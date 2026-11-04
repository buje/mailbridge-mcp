package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/edouard-claude/mailbridge-mcp/internal/auth"
	"github.com/edouard-claude/mailbridge-mcp/internal/config"
	imappool "github.com/edouard-claude/mailbridge-mcp/internal/imap"
	"github.com/edouard-claude/mailbridge-mcp/internal/signature"
	"github.com/edouard-claude/mailbridge-mcp/internal/tools"
	"github.com/mark3labs/mcp-go/server"
)

var version = "dev"

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
		server.WithInstructions("Email bridge for reading, searching, and sending emails across multiple accounts. Credentials are stored securely in macOS Keychain."),
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

	// Optional: import an email signature from Apple Mail (one-time copy).
	if sigCfg, ok := importSignatureInteractive(reader, email, accountID); ok {
		newAcc.Signature = sigCfg
	}

	replaced := false
	for i, acc := range cfg.Accounts {
		if acc.ID == accountID {
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

// runSignature attaches (or replaces) a signature on an account that is already
// configured, without redoing the full setup. The account can be given as an
// argument or picked from a list.
func runSignature(accountID string) {
	reader := bufio.NewReader(os.Stdin)

	home, _ := os.UserHomeDir()
	configDir := filepath.Join(home, ".config", "mailbridge")
	configPath := filepath.Join(configDir, "accounts.json")

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

	// Resolve the target account.
	idx := -1
	if accountID != "" {
		for i := range cfg.Accounts {
			if cfg.Accounts[i].ID == accountID {
				idx = i
				break
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
			if acc.Signature.File != "" {
				current = fmt.Sprintf(" (current: %s)", acc.Signature.File)
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
	sigCfg, ok := chooseAndImportSignature(reader, acc.Email, acc.ID)
	if !ok {
		fmt.Println("No signature set.")
		return
	}
	acc.Signature = sigCfg

	out, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(configPath, out, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", configPath, err)
		os.Exit(1)
	}
	fmt.Printf("Signature set for %q (%s). Restart the MCP (serve) to use it.\n", acc.ID, acc.Email)
}

// importSignatureInteractive asks whether to import an Apple Mail signature and,
// if so, runs the selection flow. Used by setup.
func importSignatureInteractive(reader *bufio.Reader, email, accountID string) (config.SignatureConfig, bool) {
	fmt.Print("Import a signature from the Mail app? [y/N]: ")
	ans, _ := reader.ReadString('\n')
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ans)), "y") {
		return config.SignatureConfig{}, false
	}
	return chooseAndImportSignature(reader, email, accountID)
}

// chooseAndImportSignature lists the Apple Mail signatures, suggests one based on
// the account email, imports the chosen one into the mailbridge directory and
// returns the config to store. Returns (_, false) when nothing is selected.
func chooseAndImportSignature(reader *bufio.Reader, email, accountID string) (config.SignatureConfig, bool) {
	sigs, scanned, warnings := signature.DiscoverAppleMail()
	for _, d := range scanned {
		fmt.Printf("Reading Apple Mail signatures from %s\n", d)
	}
	// Surface privacy blocks: when Mail's local folder is missing, signatures
	// may come from a secondary (possibly stale) copy.
	if len(warnings) > 0 && len(sigs) > 0 {
		fmt.Println("Warning:")
		for _, w := range warnings {
			fmt.Printf("  - %s\n", w)
		}
		fmt.Println("  Mail's local folder was not found: these signatures come from a secondary copy.")
	}
	if len(sigs) == 0 {
		fmt.Println("No Apple Mail signature found.")
		for _, w := range warnings {
			fmt.Printf("  - %s\n", w)
			if w == signature.ErrMailAccessDenied.Error() {
				fmt.Println("    macOS grants access to Mail data per application. Run this command from")
				fmt.Println("    an app that can read ~/Library/Mail (e.g. Terminal), or allow the current")
				fmt.Println("    app in System Settings > Privacy & Security > Full Disk Access.")
			}
		}
		return config.SignatureConfig{}, false
	}

	suggest := signature.SuggestForEmail(sigs, email)
	fmt.Println("Signatures found:")
	for i, s := range sigs {
		marker := ""
		if i == suggest {
			marker = fmt.Sprintf("  <- suggested for %s", email)
			for _, a := range s.Accounts {
				if strings.EqualFold(a, email) {
					marker = fmt.Sprintf("  <- used by %s in Mail", email)
				}
			}
		}
		id := strings.TrimSuffix(filepath.Base(s.SourcePath), filepath.Ext(s.SourcePath))
		if len(id) > 8 {
			id = id[:8]
		}
		date := ""
		if info, err := os.Stat(s.SourcePath); err == nil {
			date = ", " + info.ModTime().Format("02/01/2006")
		}
		fmt.Printf("  [%d] %s (%s%s)%s\n", i+1, s.Preview, id, date, marker)
	}

	def := ""
	if suggest >= 0 {
		def = strconv.Itoa(suggest + 1)
		fmt.Printf("Signature number to use [%s]: ", def)
	} else {
		fmt.Print("Signature number to use (empty for none): ")
	}
	choice, _ := reader.ReadString('\n')
	choice = strings.TrimSpace(choice)
	if choice == "" {
		choice = def
	}
	n, err := strconv.Atoi(choice)
	if err != nil || n < 1 || n > len(sigs) {
		if choice != "" {
			fmt.Println("Invalid choice, no signature linked.")
		}
		return config.SignatureConfig{}, false
	}

	rel, err := signature.Import(sigs[n-1].SourcePath, accountID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Signature import failed: %v\n", err)
		return config.SignatureConfig{}, false
	}
	fmt.Printf("Signature copied into the mailbridge directory (%s).\n", rel)
	return config.SignatureConfig{File: rel}, true
}
