package common

// JSONPatchOperation is a struct that defines PATCH operations on
// a JSON structure.
type JSONPatchOperation struct {
	Operation string      `json:"op"`
	Path      string      `json:"path"`
	Value     interface{} `json:"value,omitempty"`
}
