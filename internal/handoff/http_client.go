package handoff

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

func authenticatedRead(ctx context.Context, cfg clientConfig, endpoint string) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode != http.StatusBadGateway && resp.StatusCode != http.StatusServiceUnavailable && resp.StatusCode != http.StatusGatewayTimeout {
			return resp, nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		if attempt == 1 {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("server remained unavailable after retry")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("server read failed")
}
