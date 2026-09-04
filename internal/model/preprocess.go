package model

const PreprocessActionHomebrew = "Homebrew"

// PreprocessProgress is a presentation-independent lifecycle event emitted by
// one global action before application workers start. Raw errors deliberately
// remain in the redacted run log and do not cross into presentation layers.
type PreprocessProgress struct {
	Action  string
	Subject string
	Status  string
}
