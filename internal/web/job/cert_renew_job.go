package job

import (
	"context"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/certissue"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// CertRenewJob renews the certificates the panel issued before they expire.
// It only fetches the new files: the cisco sidecar notices the change itself,
// because its restart fingerprint covers the certificate's content.
type CertRenewJob struct{}

// NewCertRenewJob returns the renewal job.
func NewCertRenewJob() *CertRenewJob {
	return &CertRenewJob{}
}

// Run renews whatever is due and logs what could not be renewed. A failure is
// retried on the next run; with a month of margin there are many.
func (j *CertRenewJob) Run() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	for _, err := range certissue.RenewDue(ctx, time.Now()) {
		logger.Warningf("certificate renewal: %v", err)
	}
}
