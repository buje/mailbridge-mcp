package imap

import (
	"fmt"
	"strings"
	"time"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// MoveEmail moves an email from one mailbox to another.
func MoveEmail(c *imapclient.Client, fromMailbox string, uid uint32, toMailbox string) error {
	if _, err := c.Select(fromMailbox, nil).Wait(); err != nil {
		return fmt.Errorf("select %s: %w", fromMailbox, err)
	}

	uidSet := goimap.UIDSet{}
	uidSet.AddNum(goimap.UID(uid))

	if _, err := c.Move(uidSet, toMailbox).Wait(); err != nil {
		return fmt.Errorf("move UID %d from %s to %s: %w", uid, fromMailbox, toMailbox, err)
	}

	return nil
}

// MarkEmail changes flags on an email.
func MarkEmail(c *imapclient.Client, mailbox string, uid uint32, action string) error {
	if _, err := c.Select(mailbox, nil).Wait(); err != nil {
		return fmt.Errorf("select %s: %w", mailbox, err)
	}

	uidSet := goimap.UIDSet{}
	uidSet.AddNum(goimap.UID(uid))

	var storeFlags *goimap.StoreFlags
	switch action {
	case "read":
		storeFlags = &goimap.StoreFlags{
			Op:     goimap.StoreFlagsAdd,
			Flags:  []goimap.Flag{goimap.FlagSeen},
			Silent: true,
		}
	case "unread":
		storeFlags = &goimap.StoreFlags{
			Op:     goimap.StoreFlagsDel,
			Flags:  []goimap.Flag{goimap.FlagSeen},
			Silent: true,
		}
	case "flag":
		storeFlags = &goimap.StoreFlags{
			Op:     goimap.StoreFlagsAdd,
			Flags:  []goimap.Flag{goimap.FlagFlagged},
			Silent: true,
		}
	case "unflag":
		storeFlags = &goimap.StoreFlags{
			Op:     goimap.StoreFlagsDel,
			Flags:  []goimap.Flag{goimap.FlagFlagged},
			Silent: true,
		}
	default:
		return fmt.Errorf("unknown action: %q (expected read, unread, flag, unflag)", action)
	}

	if err := c.Store(uidSet, storeFlags, nil).Close(); err != nil {
		return fmt.Errorf("store flags UID %d: %w", uid, err)
	}

	return nil
}

// ListMailboxes returns the list of mailbox names for the account.
func ListMailboxes(c *imapclient.Client) ([]string, error) {
	mailboxes, err := c.List("", "*", nil).Collect()
	if err != nil {
		return nil, fmt.Errorf("IMAP list: %w", err)
	}

	names := make([]string, 0, len(mailboxes))
	for _, mb := range mailboxes {
		names = append(names, mb.Mailbox)
	}
	return names, nil
}

// CreateMailbox creates a new mailbox folder.
func CreateMailbox(c *imapclient.Client, name string) error {
	if err := c.Create(name, nil).Wait(); err != nil {
		return fmt.Errorf("create mailbox %s: %w", name, err)
	}
	return nil
}

// DeleteMailbox deletes an existing mailbox folder. The folder must be empty.
func DeleteMailbox(c *imapclient.Client, name string) error {
	if err := c.Delete(name).Wait(); err != nil {
		return fmt.Errorf("delete mailbox %s: %w", name, err)
	}
	return nil
}

// RenameMailbox renames an existing mailbox folder.
func RenameMailbox(c *imapclient.Client, oldName, newName string) error {
	if err := c.Rename(oldName, newName).Wait(); err != nil {
		return fmt.Errorf("rename mailbox %s to %s: %w", oldName, newName, err)
	}
	return nil
}

// CopyEmail copies an email from one mailbox to another.
func CopyEmail(c *imapclient.Client, fromMailbox string, uid uint32, toMailbox string) error {
	if _, err := c.Select(fromMailbox, nil).Wait(); err != nil {
		return fmt.Errorf("select %s: %w", fromMailbox, err)
	}

	uidSet := goimap.UIDSet{}
	uidSet.AddNum(goimap.UID(uid))

	if _, err := c.Copy(uidSet, toMailbox).Wait(); err != nil {
		return fmt.Errorf("copy UID %d from %s to %s: %w", uid, fromMailbox, toMailbox, err)
	}
	return nil
}

// DeleteEmail permanently deletes an email (STORE \Deleted + UID EXPUNGE).
func DeleteEmail(c *imapclient.Client, mailbox string, uid uint32) error {
	if _, err := c.Select(mailbox, nil).Wait(); err != nil {
		return fmt.Errorf("select %s: %w", mailbox, err)
	}

	uidSet := goimap.UIDSet{}
	uidSet.AddNum(goimap.UID(uid))

	storeFlags := &goimap.StoreFlags{
		Op:     goimap.StoreFlagsAdd,
		Flags:  []goimap.Flag{goimap.FlagDeleted},
		Silent: true,
	}
	if err := c.Store(uidSet, storeFlags, nil).Close(); err != nil {
		return fmt.Errorf("store \\Deleted flag UID %d: %w", uid, err)
	}

	if err := c.UIDExpunge(uidSet).Close(); err != nil {
		return fmt.Errorf("expunge UID %d: %w", uid, err)
	}

	return nil
}

// AppendMessage appends a raw message to a mailbox with the given flags.
func AppendMessage(c *imapclient.Client, mailbox string, flags []goimap.Flag, msg []byte) error {
	cmd := c.Append(mailbox, int64(len(msg)), &goimap.AppendOptions{
		Flags: flags,
		Time:  time.Now(),
	})
	if _, err := cmd.Write(msg); err != nil {
		return fmt.Errorf("append write to %s: %w", mailbox, err)
	}
	if err := cmd.Close(); err != nil {
		return fmt.Errorf("append close: %w", err)
	}
	if _, err := cmd.Wait(); err != nil {
		return fmt.Errorf("append to %s: %w", mailbox, err)
	}
	return nil
}

// FindSentMailbox detects the Sent folder for the account. It first checks
// for the \Sent special-use attribute (RFC 6154), then falls back to common names.
func FindSentMailbox(c *imapclient.Client) (string, error) {
	return findSpecialMailbox(c, goimap.MailboxAttrSent, "sent", "sent messages", "sent items", "envoyés", "éléments envoyés")
}

// FindDraftsMailbox detects the Drafts folder for the account, like FindSentMailbox.
func FindDraftsMailbox(c *imapclient.Client) (string, error) {
	return findSpecialMailbox(c, goimap.MailboxAttrDrafts, "drafts", "draft", "brouillons")
}

func findSpecialMailbox(c *imapclient.Client, attr goimap.MailboxAttr, names ...string) (string, error) {
	mailboxes, err := c.List("", "*", &goimap.ListOptions{
		ReturnSpecialUse: true,
	}).Collect()
	if err != nil {
		// Fallback if SPECIAL-USE not supported
		mailboxes, err = c.List("", "*", nil).Collect()
		if err != nil {
			return "", fmt.Errorf("IMAP list: %w", err)
		}
	}

	// Candidates: folders flagged with the special-use attribute, and folders
	// whose last path component is a known name. Several can exist, e.g. on
	// OVH another client created "INBOX.INBOX.Drafts" (flagged \Drafts by the
	// server) while Apple Mail uses "INBOX.Drafts".
	var cands []mailboxCandidate
	for _, mb := range mailboxes {
		special := false
		for _, a := range mb.Attrs {
			if a == attr {
				special = true
			}
		}
		parts := []string{mb.Mailbox}
		if mb.Delim != 0 {
			parts = strings.Split(mb.Mailbox, string(mb.Delim))
		}
		last := strings.ToLower(parts[len(parts)-1])
		rank := len(names)
		for i, name := range names {
			if last == name {
				rank = i
				break
			}
		}
		if special || rank < len(names) {
			cands = append(cands, mailboxCandidate{name: mb.Mailbox, depth: len(parts), special: special, nameRank: rank})
		}
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("no %s mailbox found", strings.TrimPrefix(string(attr), "\\"))
	}
	return pickMailbox(cands), nil
}

type mailboxCandidate struct {
	name     string
	depth    int  // number of path components ("INBOX.Drafts" = 2)
	special  bool // flagged with the special-use attribute
	nameRank int  // position of the name in the known names list (len(names) if none)
}

// pickMailbox chooses among several candidate folders: the least nested one
// (a duplicate such as "INBOX.INBOX.Drafts" is created by a misconfigured
// client), then the special-use one, then the most usual name. Message counts
// are not a reliable signal: drafts disappear once sent or deleted.
func pickMailbox(cands []mailboxCandidate) string {
	best := cands[0]
	for _, c := range cands[1:] {
		switch {
		case c.depth != best.depth:
			if c.depth < best.depth {
				best = c
			}
		case c.special != best.special:
			if c.special {
				best = c
			}
		case c.nameRank != best.nameRank:
			if c.nameRank < best.nameRank {
				best = c
			}
		case c.name < best.name:
			best = c
		}
	}
	return best.name
}

// MailboxStatusInfo holds status information about a mailbox.
type MailboxStatusInfo struct {
	Mailbox  string
	Messages uint32
	Unseen   uint32
}

// MailboxStatus returns status information about a mailbox (messages, unseen counts).
// Note: NumDeleted is not requested because many servers (e.g. OVH, Dovecot without IMAP4rev2)
// return BAD for the DELETED status item.
func MailboxStatus(c *imapclient.Client, mailbox string) (*MailboxStatusInfo, error) {
	data, err := c.Status(mailbox, &goimap.StatusOptions{
		NumMessages: true,
		NumUnseen:   true,
	}).Wait()
	if err != nil {
		return nil, fmt.Errorf("status %s: %w", mailbox, err)
	}

	info := &MailboxStatusInfo{
		Mailbox: data.Mailbox,
	}
	if data.NumMessages != nil {
		info.Messages = *data.NumMessages
	}
	if data.NumUnseen != nil {
		info.Unseen = *data.NumUnseen
	}
	return info, nil
}
