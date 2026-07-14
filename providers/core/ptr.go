package core

// Ptr returns a pointer to v. It is a convenience for building requests whose
// optional fields are pointers (e.g. streaming tool-call indexes).
func Ptr[T any](v T) *T { return &v }
