package edit

// SetFields lists the node fields the set operation changes.
func SetFields() []string { return append([]string{}, setFields...) }

// UnsetFields lists the node fields the unset operation removes.
func UnsetFields() []string { return append([]string{}, unsetFields...) }
