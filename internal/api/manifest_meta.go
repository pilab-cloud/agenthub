package api

import "encoding/json"

// manifestMeta extracts the user-facing fields the web catalog needs from a
// published manifest. Unknown or missing fields stay empty.
type manifestMeta struct {
	DisplayName string `json:"displayName"`
	Summary     string `json:"summary"`
	Icon        string `json:"icon"`
}

func parseManifestMeta(manifest []byte) manifestMeta {
	var m manifestMeta
	_ = json.Unmarshal(manifest, &m)
	return m
}
