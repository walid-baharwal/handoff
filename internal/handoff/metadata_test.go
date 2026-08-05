package handoff

import "testing"

func TestCanonicalRepositoryMatchesSSHAndHTTPS(t *testing.T) {
	sshCanonical, sshProject := canonicalRepository("git@github.com:walid-baharwal/handoff.git")
	httpsCanonical, httpsProject := canonicalRepository("https://github.com/walid-baharwal/handoff.git")
	if sshCanonical != httpsCanonical {
		t.Fatalf("repository identities differ: %q != %q", sshCanonical, httpsCanonical)
	}
	if sshProject != "handoff" || httpsProject != "handoff" {
		t.Fatalf("unexpected projects: %q, %q", sshProject, httpsProject)
	}
}

func TestCanonicalRepositoryRemovesCredentialsAndNormalizesHost(t *testing.T) {
	canonical, project := canonicalRepository("ssh://git@GitHub.COM/walid-baharwal/handoff.git")
	if canonical != "github.com/walid-baharwal/handoff" {
		t.Fatalf("canonical repository = %q", canonical)
	}
	if project != "handoff" {
		t.Fatalf("project = %q", project)
	}
}
