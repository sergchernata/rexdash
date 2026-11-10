// Package awsx loads the AWS config for the AWS panes, with a request timeout so a stalled
// call fails instead of freezing the pane. Kept apart from netx so rexdash doesn't link the SDK.
package awsx

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"

	"rexdash/internal/netx"
)

// Config is the default credential chain (AWS_PROFILE, AWS_REGION, ~/.aws) with a timeout.
func Config(ctx context.Context) (aws.Config, error) { return awsConfig(ctx, netx.Timeout) }

func awsConfig(ctx context.Context, d time.Duration) (aws.Config, error) {
	return config.LoadDefaultConfig(ctx, config.WithHTTPClient(awshttp.NewBuildableClient().WithTimeout(d)))
}
