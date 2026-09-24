package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/sholdee/drydock/internal/prcomment"
	"github.com/spf13/cobra"
)

const (
	// defaultPRCommentTokenEnv is consulted first; prCommentFallbackTokenEnv
	// covers CI runners that only export the platform's own variable.
	defaultPRCommentTokenEnv   = "DRYDOCK_GITHUB_TOKEN"
	prCommentFallbackTokenEnv  = "GITHUB_TOKEN"
	defaultPRCommentAPIURL     = "https://api.github.com"
	prCommentAPIURLEnv         = "GITHUB_API_URL"
	prCommentRepositoryEnv     = "GITHUB_REPOSITORY"
	defaultPRCommentTimeout    = 30 * time.Second
	prCommentStdinBodyFilePath = "-"
)

type prCommentFlags struct {
	apiURL     string
	repository string
	number     int
	messageID  string
	bodyFile   string
	tokenEnv   string
	timeout    time.Duration
}

func newPRCommentCommand(info VersionInfo) *cobra.Command {
	flags := prCommentFlags{
		tokenEnv: defaultPRCommentTokenEnv,
		timeout:  defaultPRCommentTimeout,
	}
	cmd := &cobra.Command{
		Use:   "comment",
		Short: "Post or update a sticky pull request comment",
		Long: "Post or update a sticky pull request comment through the host SCM's\n" +
			"GitHub-compatible issue-comment API (GitHub, Forgejo, Gitea). The comment\n" +
			"is identified by --message-id, so repeated runs edit the same comment in\n" +
			"place. The token is read from the environment only, never from argv.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPRComment(cmd, info, flags)
		},
	}
	bindPRCommentFlags(cmd, &flags)
	return cmd
}

func bindPRCommentFlags(cmd *cobra.Command, flags *prCommentFlags) {
	cmd.Flags().StringVar(&flags.apiURL, "api-url", flags.apiURL,
		"SCM API base URL (default $"+prCommentAPIURLEnv+", else "+defaultPRCommentAPIURL+")")
	cmd.Flags().StringVar(&flags.repository, "repository", flags.repository,
		"target repository as owner/name (default $"+prCommentRepositoryEnv+")")
	cmd.Flags().IntVar(&flags.number, "number", flags.number, "pull request number")
	cmd.Flags().StringVar(&flags.messageID, "message-id", flags.messageID,
		"sticky comment identifier, e.g. 12/drydock-diff")
	cmd.Flags().StringVar(&flags.bodyFile, "body-file", flags.bodyFile,
		"file holding the comment body, or "+prCommentStdinBodyFilePath+" for stdin")
	cmd.Flags().StringVar(&flags.tokenEnv, "token-env", flags.tokenEnv,
		"environment variable holding the API token; the default falls back to $"+prCommentFallbackTokenEnv)
	cmd.Flags().DurationVar(&flags.timeout, "timeout", flags.timeout, "overall timeout for the API calls")
	for _, name := range []string{"number", "message-id", "body-file"} {
		// MarkFlagRequired only fails on an unknown flag name, which the
		// command's own tests catch long before a release.
		_ = cmd.MarkFlagRequired(name)
	}
}

func runPRComment(cmd *cobra.Command, info VersionInfo, flags prCommentFlags) error {
	repository, err := prCommentRepository(flags)
	if err != nil {
		return err
	}
	messageID, err := prCommentMessageID(flags)
	if err != nil {
		return err
	}
	if flags.number <= 0 {
		return fmt.Errorf("invalid --number %d: want a positive pull request number", flags.number)
	}
	if flags.timeout <= 0 {
		return fmt.Errorf("invalid --timeout %s: want a positive duration", flags.timeout)
	}
	token, err := prCommentToken(flags.tokenEnv, cmd.Flags().Changed("token-env"))
	if err != nil {
		return err
	}
	message, err := prCommentMessage(cmd, flags.bodyFile)
	if err != nil {
		return err
	}

	client := prcomment.Client{
		BaseURL:   prCommentAPIURL(flags),
		Token:     token,
		UserAgent: prCommentUserAgent(info),
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), flags.timeout)
	defer cancel()
	result, err := client.Upsert(ctx, repository, flags.number, messageID, message)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", result.Action, prCommentTarget(result))
	return err
}

func prCommentAPIURL(flags prCommentFlags) string {
	if apiURL := strings.TrimSpace(flags.apiURL); apiURL != "" {
		return apiURL
	}
	if apiURL := strings.TrimSpace(os.Getenv(prCommentAPIURLEnv)); apiURL != "" {
		return apiURL
	}
	return defaultPRCommentAPIURL
}

func prCommentRepository(flags prCommentFlags) (string, error) {
	repository := strings.TrimSpace(flags.repository)
	if repository == "" {
		repository = strings.TrimSpace(os.Getenv(prCommentRepositoryEnv))
	}
	if repository == "" {
		return "", errors.New("no repository: pass --repository or set $" + prCommentRepositoryEnv)
	}
	return repository, nil
}

func prCommentMessageID(flags prCommentFlags) (string, error) {
	messageID := flags.messageID
	if messageID == "" || strings.ContainsFunc(messageID, unicode.IsSpace) {
		return "", fmt.Errorf("invalid --message-id %q: want a non-empty value without whitespace", messageID)
	}
	return messageID, nil
}

// prCommentToken reads the token from the environment only: a token on argv
// would land in the runner's process list and in CI command traces. The
// implicit $GITHUB_TOKEN fallback applies only when the caller did not name a
// variable: an explicit --token-env pointing at an empty variable must fail
// rather than send a GitHub-issued credential to whatever --api-url names.
func prCommentToken(tokenEnv string, explicit bool) (string, error) {
	names := []string{tokenEnv}
	if !explicit && tokenEnv != prCommentFallbackTokenEnv {
		names = append(names, prCommentFallbackTokenEnv)
	}
	for _, name := range names {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token, nil
		}
	}
	return "", fmt.Errorf("no token: set $%s", strings.Join(names, " or $"))
}

func prCommentMessage(cmd *cobra.Command, bodyFile string) (string, error) {
	if bodyFile == prCommentStdinBodyFilePath {
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read comment body from stdin: %w", err)
		}
		return string(data), nil
	}
	data, err := os.ReadFile(bodyFile)
	if err != nil {
		return "", fmt.Errorf("read comment body: %w", err)
	}
	return string(data), nil
}

func prCommentUserAgent(info VersionInfo) string {
	version := strings.TrimSpace(info.Version)
	if version == "" {
		version = "dev"
	}
	return "drydock/" + version
}

func prCommentTarget(result prcomment.Result) string {
	if result.URL != "" {
		return result.URL
	}
	return strconv.FormatInt(result.ID, 10)
}
