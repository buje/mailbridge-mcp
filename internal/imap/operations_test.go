package imap

import "testing"

func TestPickMailbox(t *testing.T) {
	tests := []struct {
		name  string
		cands []mailboxCandidate
		want  string
	}{
		{
			name: "OVH support@: flagged duplicate INBOX.INBOX.Drafts, Mail uses INBOX.Drafts",
			cands: []mailboxCandidate{
				{name: "INBOX.INBOX.Drafts", depth: 3, special: true, nameRank: 0},
				{name: "INBOX.Drafts", depth: 2, nameRank: 0},
				{name: "INBOX.Brouillons", depth: 2, nameRank: 2},
			},
			want: "INBOX.Drafts",
		},
		{
			name: "OVH thomas@: INBOX.Drafts flagged, INBOX.Brouillons from another client",
			cands: []mailboxCandidate{
				{name: "INBOX.Brouillons", depth: 2, nameRank: 2},
				{name: "INBOX.Drafts", depth: 2, special: true, nameRank: 0},
				{name: "INBOX.INBOX.Drafts", depth: 3, nameRank: 0},
			},
			want: "INBOX.Drafts",
		},
		{
			name: "Sent: Apple Mail's INBOX.Sent Messages beats the flagged duplicate",
			cands: []mailboxCandidate{
				{name: "INBOX.INBOX.Sent", depth: 3, special: true, nameRank: 0},
				{name: "INBOX.Sent Messages", depth: 2, nameRank: 1},
			},
			want: "INBOX.Sent Messages",
		},
		{
			name: "Gmail: only the flagged folder",
			cands: []mailboxCandidate{
				{name: "[Gmail]/Brouillons", depth: 2, special: true, nameRank: 2},
			},
			want: "[Gmail]/Brouillons",
		},
		{
			name: "same depth, no flag: most usual name",
			cands: []mailboxCandidate{
				{name: "Brouillons", depth: 1, nameRank: 2},
				{name: "Drafts", depth: 1, nameRank: 0},
			},
			want: "Drafts",
		},
	}
	for _, tt := range tests {
		if got := pickMailbox(tt.cands); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}
