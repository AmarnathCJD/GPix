package bridge

import (
	"io"
	"os"

	"gpix/pkg/disguise"
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

func wrapTGFile(tempDir, srcPath, name, passphrase string) (string, error) {
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
	var wrapped io.Reader
	if passphrase != "" {
		wrapped, _, err = disguise.WrapEncrypted(name, src, passphrase)
		if err != nil {
			os.Remove(out.Name())
			return "", err
		}
	} else {
		wrapped, _ = disguise.Wrap(name, src, st.Size())
	}
	if _, err := io.Copy(out, wrapped); err != nil {
		os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

func unwrapTGFile(tempDir, srcPath, passphrase string) (extractedPath, origName string, err error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return "", "", err
	}
	defer src.Close()
	hdr, payload, err := disguise.ExtractWithPassphrase(src, passphrase)
	if err != nil {
		return "", "", err
	}
	out, err := os.CreateTemp(tempDir, "gpix-unwrap-*")
	if err != nil {
		return "", "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, payload); err != nil {
		os.Remove(out.Name())
		return "", "", err
	}
	return out.Name(), hdr.Filename, nil
}
