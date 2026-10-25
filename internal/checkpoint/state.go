package checkpoint

import "regexp"

var validStateFile = regexp.MustCompile(`^state-[a-f0-9]{32}\.(vmrs|vmgs|yaml)$`)

func safeArtifact(name string) bool { return safeFile(name) || validStateFile.MatchString(name) }

// StateFiles contains only this recovery point's immutable state files.
func (e Entry) StateFiles() []string {
	if e.State == "" {
		return nil
	}
	files := []string{"state-" + e.ID + ".vmgs", "state-" + e.ID + ".yaml"}
	if e.State != "off" {
		files = append(files, "state-"+e.ID+".vmrs")
	}
	return files
}
