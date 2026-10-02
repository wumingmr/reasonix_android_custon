// Package winaclresidue cleans up ACL residue that the retired Windows sandbox
// backend (v1.38.8 to v1.38.10) left on user files. That backend applied
// temporary deny and grant ACEs and recorded each mutation in a per-process
// marker under %TEMP%; a crash left both behind. This package removes only
// those exact trustees from paths named in markers whose owner is provably
// gone, and repairs the one legacy DENY RX entry on the credential store when
// a dead-run marker proves its origin. It never creates markers or mutates
// ACLs for any other reason. Non-Windows builds are no-ops.
package winaclresidue
