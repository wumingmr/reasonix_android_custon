package persistentshell

type ptyConn interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
}
