package backend

import "fmt"

// refuseArm is the defence-in-depth half of the ablation contract.
//
// Ablation arms (rank profile, embedder presence, mandatory addressing) are
// properties of CAIRN. A baseline has no rank profile to switch and no digest
// inclusion classes to turn on, so asking B0/B1/B2 for one is a bug in the
// caller — and the dangerous version of that bug is the silent one, where the
// backend runs its ordinary default and the runner records the result under
// the arm's name. That would be a fabricated ablation result, indistinguishable
// from a real one in the output file.
//
// The runner already restricts arms to the Cairn backend. This is the second
// lock, in the place that actually knows what it can and cannot do.
func refuseArm(id ID, arm ArmConfig) error {
	if arm.IsDefault() {
		return nil
	}
	return fmt.Errorf("%w: %s has no ranking profile, no embedder and no inclusion classes, so it cannot realize %q; running it anyway would record a default run under an ablation's name",
		ErrArmUnrealizable, id, arm.ID)
}
