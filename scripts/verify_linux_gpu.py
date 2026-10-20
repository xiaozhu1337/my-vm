import ctypes as C
import json, uuid, time, traceback
from pathlib import Path

cu=C.CDLL('/usr/lib/wsl/lib/libcuda.so.1')
def call(name,*args):
    print('CALL',name,flush=True)
    result=getattr(cu,name)(*args)
    if result:
        message=C.c_char_p()
        cu.cuGetErrorString(result,C.byref(message))
        raise RuntimeError(f'{name}: CUDA {result}, {message.value!r}')

ptx=b'''
.version 8.0
.target sm_80
.address_size 64
.visible .entry add(.param .u64 pa,.param .u64 pb,.param .u64 pc) {
    .reg .b32 %i, %a, %b, %c;
    .reg .b64 %ap, %bp, %cp, %off;
    ld.param.u64 %ap, [pa];
    ld.param.u64 %bp, [pb];
    ld.param.u64 %cp, [pc];
    mov.u32 %i, %tid.x;
    mul.wide.u32 %off, %i, 4;
    add.u64 %ap, %ap, %off;
    add.u64 %bp, %bp, %off;
    add.u64 %cp, %cp, %off;
    ld.global.u32 %a, [%ap];
    ld.global.u32 %b, [%bp];
    add.u32 %c, %a, %b;
    st.global.u32 [%cp], %c;
    ret;
}
'''

call('cuInit',C.c_uint(0))
count=C.c_int();call('cuDeviceGetCount',C.byref(count))
if count.value<=0:raise RuntimeError('No CUDA device')
print('DEVICE_COUNT',count.value,flush=True)
device=C.c_int();call('cuDeviceGet',C.byref(device),C.c_int(0))
name=C.create_string_buffer(256);call('cuDeviceGetName',name,C.c_int(len(name)),device)
identity=(C.c_ubyte*16)();call('cuDeviceGetUuid_v2',C.byref(identity),device)
major,minor=C.c_int(),C.c_int();call('cuDeviceComputeCapability',C.byref(major),C.byref(minor),device)
version=C.c_int();call('cuDriverGetVersion',C.byref(version))
report={'device':name.value.decode(),'uuid':'GPU-'+str(uuid.UUID(bytes=bytes(identity))),'compute_capability':[major.value,minor.value],'driver_api':version.value,'trials':[]}
print('DEVICE',json.dumps(report),flush=True)
if major.value < 8:raise RuntimeError('CUDA self-check requires compute capability 8.0 or later: '+str([major.value,minor.value]))
context,module,function=C.c_void_p(),C.c_void_p(),C.c_void_p()
pointers=[]
call('cuDevicePrimaryCtxRetain',C.byref(context),device)
try:
    call('cuCtxSetCurrent',context)
    call('cuModuleLoadData',C.byref(module),C.c_char_p(ptx))
    call('cuModuleGetFunction',C.byref(function),module,C.c_char_p(b'add'))
    n=256;size=C.c_size_t(n*C.sizeof(C.c_uint32))
    for _ in range(3):
        pointer=C.c_uint64();call('cuMemAlloc_v2',C.byref(pointer),size);pointers.append(pointer)
    arguments=(C.c_void_p*3)(*(C.addressof(p) for p in pointers))
    for trial in range(3):
        a=(C.c_uint32*n)(*range(n));b=(C.c_uint32*n)(*(3*i+trial for i in range(n)))
        output=(C.c_uint32*n)(*([0xDEADBEEF]*n))
        call('cuMemcpyHtoD_v2',pointers[0],C.byref(a),size)
        call('cuMemcpyHtoD_v2',pointers[1],C.byref(b),size)
        call('cuMemcpyHtoD_v2',pointers[2],C.byref(output),size)
        start=time.perf_counter()
        call('cuLaunchKernel',function,C.c_uint(1),C.c_uint(1),C.c_uint(1),C.c_uint(n),C.c_uint(1),C.c_uint(1),C.c_uint(0),C.c_void_p(),arguments,C.c_void_p())
        call('cuCtxSynchronize')
        call('cuMemcpyDtoH_v2',C.byref(output),pointers[2],size)
        elapsed=time.perf_counter()-start
        expected=[a[i]+b[i] for i in range(n)]
        mismatches=sum(actual!=wanted for actual,wanted in zip(output,expected))
        if mismatches:raise RuntimeError(f'trial={trial+1}, mismatches={mismatches}, actual={list(output)}, expected={expected}')
        result={'trial':trial+1,'elements':n,'mismatches':mismatches,'first':output[0],'last':output[-1],'checksum':sum(output),'host_launch_sync_copy_ms':elapsed*1000}
        report['trials'].append(result);print('PASS',json.dumps(result),flush=True)
finally:
    for resource,args in [('cuMemFree_v2',(p,)) for p in pointers]+([('cuModuleUnload',(module,))] if module.value else [])+[('cuDevicePrimaryCtxRelease_v2',(device,))]:
        result=getattr(cu,resource)(*args)
        if result:print('CLEANUP_ERROR',resource,result,flush=True)
Path('/tmp/gpu-worker-cuda-result.json').write_text(json.dumps(report,indent=2),encoding='utf-8')
print('PASS CUDA GPU vector addition, all three trials verified',flush=True)
