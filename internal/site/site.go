// Package site is the home of the comprehensive teardown helper used
// by every site-delete code path. It's deliberately a small package
// post-v0.2.52: the v0.2.47-era site.Manager.Create / Delete /
// ReapplyRuntime orchestrators are gone — every site-lifecycle event
// now flows through internal/site/creator. See:
//
//   internal/site/creator/run.go     — RunCreate dispatcher + smoke probe
//   internal/site/creator/delete.go  — RunDelete (fs artifacts sweep)
//   internal/site/creator/reapply.go — RunReapply + RunReapplyRuntime
//   internal/site/teardown.go        — Teardown helper (this package)
//
// internal/site/teardown.go is what remains. The Teardown function +
// TeardownDeps struct are called from the API layer's
// deleteSiteViaNewPipeline (api/sites_creator.go) after
// creator.RunDelete + the type-specific backend removal.
//
// docs/CLOUDPANEL-STUDY.md → "What this maps to in auraCP" §
// records the original migration plan that led here. v0.2.52 closed
// the last legacy code path.
package site
