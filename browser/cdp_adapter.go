package browser

import (
	"context"

	"github.com/PolymuxOrg/midas/cdp"
)

type cdpRootConn struct {
	conn *cdp.Conn
}

func (c *cdpRootConn) Send(ctx context.Context, method string, params any, result any) error {
	return c.conn.Send(ctx, method, params, result)
}

func (c *cdpRootConn) On(event string, handler cdp.EventHandler) cdp.Unsubscribe {
	return c.conn.On(event, handler)
}

func (c *cdpRootConn) Close() error {
	return c.conn.Close()
}

func (c *cdpRootConn) EnableAutoAttach(ctx context.Context) error {
	return c.conn.EnableAutoAttach(ctx)
}

func (c *cdpRootConn) AttachToTarget(ctx context.Context, targetID string) (sessionLike, error) {
	return c.conn.AttachToTarget(ctx, targetID)
}

func (c *cdpRootConn) GetSession(sessionID string) (sessionLike, bool) {
	return c.conn.GetSession(sessionID)
}

func (c *cdpRootConn) GetTargets(ctx context.Context) ([]cdp.TargetInfo, error) {
	return c.conn.GetTargets(ctx)
}
