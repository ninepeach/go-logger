//go:build !linux && !darwin

package logger

import "time"

func dialLocal(string, time.Duration) (syslogConn, error) {
	return syslogConn{}, ErrSyslogLocalUnsupported
}

func classifyDatagram(error) datagramAction { return datagramDisconnect }
