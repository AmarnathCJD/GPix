package immich

import (
	"io"
	"os"

	"gpix/pkg/disguise"
	"gpix/pkg/mediacrypt"
)

func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	read, err := io.ReadFull(f, buf)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		return buf[:read], nil
	}
	if err != nil {
		return nil, err
	}
	return buf[:read], nil
}

func encryptToTemp(tempDir, srcPath, name string, size int64, crypt *mediacrypt.Manager) (string, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	out, err := os.CreateTemp(tempDir, "gpix-enc-*")
	if err != nil {
		return "", err
	}
	defer out.Close()
	if err := crypt.Encrypt(out, src, size, name); err != nil {
		os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

func wrapToTemp(tempDir, srcPath, name string) (string, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return "", err
	}
	out, err := os.CreateTemp(tempDir, "gpix-disg-*.mp4")
	if err != nil {
		return "", err
	}
	defer out.Close()
	wrapped, _ := disguise.Wrap(name, src, st.Size())
	if _, err := io.Copy(out, wrapped); err != nil {
		os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}
