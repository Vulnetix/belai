package profiles

// WizardInput is the raw input collected by the /profile wizard.
type WizardInput struct {
	Name    string
	Content string
}

// Run executes the profile wizard's validate-and-write stage. The interactive
// prompt itself is rendered by the TUI; Run validates the collected input and
// writes a well-formed profile file.
func Run(in WizardInput) (Profile, error) {
	p := Profile{Name: in.Name, Content: in.Content}
	if err := Validate(p); err != nil {
		return Profile{}, err
	}
	if _, err := Save(p); err != nil {
		return Profile{}, err
	}
	return p, nil
}
