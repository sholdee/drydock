// Package prcomment upserts sticky pull request comments through the host
// SCM's GitHub-compatible issue-comment API (GitHub, Forgejo, Gitea). It is a
// CI helper: nothing on the render, diff, test, get, or diag paths imports it.
package prcomment
