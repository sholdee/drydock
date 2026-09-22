// Package gitauth resolves SSH credentials for Git sources: the explicit key
// file when one is configured, otherwise the ambient identities OpenSSH would
// use — the agent, ~/.ssh/config IdentityFile entries, and the default
// ~/.ssh/id_* files — always with known_hosts host-key verification.
package gitauth
