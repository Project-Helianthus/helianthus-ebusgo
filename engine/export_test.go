package engine

// DonorCollaborator returns the copied donor decisions so an external replay
// test can inject them.
func DonorCollaborator() EngineCollaborator {
	return donorCollaborator{}
}
