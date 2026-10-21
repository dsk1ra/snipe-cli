package adapt

// Draft is one rewrite of one selected bullet (PRD §6.3). Rewrite produces
// them, verify.Verify classifies them, Select picks among the survivors.
type Draft struct {
	Hash string // ledger Bullet.Hash of the source bullet
	Kind string // "A" minimal edit, "B" re-led; "" for the original
	Text string
}
