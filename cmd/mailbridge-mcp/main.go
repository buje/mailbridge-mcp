package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/edouard-claude/mailbridge-mcp/internal/applemail"
	"github.com/edouard-claude/mailbridge-mcp/internal/auth"
	"github.com/edouard-claude/mailbridge-mcp/internal/config"
	imappool "github.com/edouard-claude/mailbridge-mcp/internal/imap"
	"github.com/edouard-claude/mailbridge-mcp/internal/tools"
	"github.com/mark3labs/mcp-go/server"
)

var version = "dev"

// toolTimeout bounds a single tool call, above the SMTP session timeout.
const toolTimeout = 150 * time.Second

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: mailbridge-mcp <serve|setup|signature [account_id]>")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		runServer()
	case "setup":
		runSetup()
	case "signature":
		accountID := ""
		if len(os.Args) >= 3 {
			accountID = os.Args[2]
		}
		runSignature(accountID)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func runServer() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	pool := imappool.NewPool(cfg)
	defer pool.Close()

	s := server.NewMCPServer(
		"mailbridge-mcp",
		version,
		server.WithInstructions("Email bridge for reading, searching, and sending emails across multiple accounts. Credentials are stored securely in macOS Keychain. Replies are saved as drafts by default for user review; only send when the user explicitly asks."),
		server.WithToolHandlerMiddleware(tools.TimeoutMiddleware(pool, toolTimeout)),
	)

	tools.RegisterAll(s, cfg, pool)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runSetup() {
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Account ID (e.g. perso, pro): ")
	accountID, _ := reader.ReadString('\n')
	accountID = strings.TrimSpace(accountID)

	fmt.Print("Email: ")
	email, _ := reader.ReadString('\n')
	email = strings.TrimSpace(email)

	fmt.Print("IMAP host (e.g. ssl0.ovh.net, imap.gmail.com): ")
	imapHost, _ := reader.ReadString('\n')
	imapHost = strings.TrimSpace(imapHost)

	smtpDefault := imapHost
	if strings.HasPrefix(imapHost, "imap.") {
		smtpDefault = "smtp." + imapHost[5:]
	}
	fmt.Printf("SMTP host [%s]: ", smtpDefault)
	smtpHost, _ := reader.ReadString('\n')
	smtpHost = strings.TrimSpace(smtpHost)
	if smtpHost == "" {
		smtpHost = smtpDefault
	}

	fmt.Print("Password: ")
	password, _ := reader.ReadString('\n')
	password = strings.TrimSpace(password)

	service := "mailbridge-mcp"
	if err := auth.SetPassword(service, email, password); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to store in Keychain: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Password stored in Keychain as %s/%s\n", service, email)

	// Load or create config
	home, _ := os.UserHomeDir()
	configDir := filepath.Join(home, ".config", "mailbridge")
	configPath := filepath.Join(configDir, "accounts.json")

	cfg := &config.Config{
		Defaults: config.Defaults{
			MaxFetch:       50,
			BodyMaxChars:   10000,
			TimeoutSeconds: 30,
		},
	}

	if data, err := os.ReadFile(configPath); err == nil {
		_ = json.Unmarshal(data, cfg)
	}

	// Replace existing account with same ID, or append
	newAcc := config.Account{
		ID:    accountID,
		Label: accountID,
		Email: email,
		IMAP: config.IMAPConfig{
			Host: imapHost,
			Port: 993,
			TLS:  true,
		},
		SMTP: config.SMTPConfig{
			Host:     smtpHost,
			Port:     587,
			StartTLS: true,
		},
		Auth: config.AuthConfig{
			Type:            "keychain",
			KeychainService: service,
			KeychainAccount: email,
		},
	}

	// Optional: import the signature from the Mail app.
	fmt.Print("Import a signature from the Mail app? [y/N]: ")
	if ans, _ := reader.ReadString('\n'); strings.HasPrefix(strings.ToLower(strings.TrimSpace(ans)), "y") {
		if sig, ok := importAppleMailSignature(reader, email, accountID); ok {
			newAcc.Signature = sig
		}
	}

	replaced := false
	for i, acc := range cfg.Accounts {
		if acc.ID == accountID {
			if newAcc.Signature == nil {
				newAcc.Signature = acc.Signature // keep the existing signature
			}
			cfg.Accounts[i] = newAcc
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.Accounts = append(cfg.Accounts, newAcc)
	}

	// Write config
	if err := os.MkdirAll(configDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating %s: %v\n", configDir, err)
		os.Exit(1)
	}

	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", configPath, err)
		os.Exit(1)
	}

	fmt.Printf("Config saved to %s\n", configPath)
	fmt.Printf("\nAccount %q (%s) ready. Run 'mailbridge-mcp serve' to start.\n", accountID, email)
}

// runSignature imports (or replaces) the signature of an account that is
// already configured, from the Mail app. The account is given as an argument
// or picked from a list.
func runSignature(accountID string) {
	reader := bufio.NewReader(os.Stdin)

	dir, err := config.Dir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config directory: %v\n", err)
		os.Exit(1)
	}
	configPath := filepath.Join(dir, "accounts.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "No config at %s. Run 'mailbridge-mcp setup' first.\n", configPath)
		os.Exit(1)
	}
	cfg := &config.Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid config %s: %v\n", configPath, err)
		os.Exit(1)
	}
	if len(cfg.Accounts) == 0 {
		fmt.Fprintln(os.Stderr, "No account configured. Run 'mailbridge-mcp setup' first.")
		os.Exit(1)
	}

	idx := -1
	if accountID != "" {
		for i := range cfg.Accounts {
			if cfg.Accounts[i].ID == accountID {
				idx = i
			}
		}
		if idx < 0 {
			fmt.Fprintf(os.Stderr, "Unknown account %q.\n", accountID)
			os.Exit(1)
		}
	} else {
		fmt.Println("Accounts:")
		for i, acc := range cfg.Accounts {
			current := ""
			if acc.Signature != nil && acc.Signature.HTMLFile != "" {
				current = " (current: " + acc.Signature.HTMLFile + ")"
			}
			fmt.Printf("  [%d] %s — %s%s\n", i+1, acc.ID, acc.Email, current)
		}
		fmt.Print("Account number: ")
		choice, _ := reader.ReadString('\n')
		n, err := strconv.Atoi(strings.TrimSpace(choice))
		if err != nil || n < 1 || n > len(cfg.Accounts) {
			fmt.Fprintln(os.Stderr, "Invalid choice.")
			os.Exit(1)
		}
		idx = n - 1
	}

	acc := &cfg.Accounts[idx]
	sig, ok := importAppleMailSignature(reader, acc.Email, acc.ID)
	if !ok {
		fmt.Println("No signature set.")
		return
	}
	acc.Signature = sig

	out, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(configPath, out, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", configPath, err)
		os.Exit(1)
	}
	fmt.Printf("Signature set for %q (%s). It is used from the next message, no restart needed.\n", acc.ID, acc.Email)
}

// importAppleMailSignature lists the Mail signatures, pre-selects the one Mail
// uses for email, and imports the chosen one in the mailbridge format.
func importAppleMailSignature(reader *bufio.Reader, email, accountID string) (*config.SignatureConfig, bool) {
	sigs, dir, err := applemail.Discover()
	if err != nil {
		fmt.Printf("Cannot read the Mail signatures: %v\n", err)
		if errors.Is(err, applemail.ErrAccessDenied) {
			fmt.Println("  macOS grants access to Mail data per application. Run this command from an app")
			fmt.Println("  that can read it (e.g. Terminal), or allow the current app in System Settings >")
			fmt.Println("  Privacy & Security > Full Disk Access. It is only needed for this import.")
		}
		return nil, false
	}
	if len(sigs) == 0 {
		fmt.Printf("No signature found in %s.\n", dir)
		return nil, false
	}

	fmt.Printf("Signatures in %s:\n", dir)
	suggest := applemail.Suggest(sigs, email)
	for i, s := range sigs {
		marker := ""
		if i == suggest {
			marker = "  <- suggested for " + email
			if s.UsedBy(email) {
				marker = "  <- used by " + email + " in Mail"
			}
		}
		fmt.Printf("  [%d] %s%s\n", i+1, s.Preview, marker)
	}

	def := ""
	if suggest >= 0 {
		def = strconv.Itoa(suggest + 1)
		fmt.Printf("Signature number [%s]: ", def)
	} else {
		fmt.Print("Signature number (empty for none): ")
	}
	choice, _ := reader.ReadString('\n')
	if choice = strings.TrimSpace(choice); choice == "" {
		choice = def
	}
	n, err := strconv.Atoi(choice)
	if err != nil || n < 1 || n > len(sigs) {
		if choice != "" {
			fmt.Println("Invalid choice.")
		}
		return nil, false
	}

	sig, skipped, err := applemail.Import(sigs[n-1].Path, accountID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Signature import failed: %v\n", err)
		return nil, false
	}
	fmt.Printf("Signature saved to ~/.config/mailbridge/%s (%d inline image(s)).\n", sig.HTMLFile, len(sig.Images))
	for _, s := range skipped {
		fmt.Printf("  image dropped (PNG, JPEG or GIF under 1 MB only): %s\n", s)
	}
	return sig, true
}
