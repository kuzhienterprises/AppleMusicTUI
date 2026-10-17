package apple

import "context"

// GetStorefront fetches the storefront of the signed-in account. It doubles
// as a cheap way to verify that a music user token is valid.
func (c *Client) GetStorefront(ctx context.Context, out any) error {
	return c.do(ctx, "/v1/me/storefront", nil, out)
}
