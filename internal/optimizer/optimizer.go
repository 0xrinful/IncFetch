package optimizer

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	MaxCwebpDim = 16380
	Quality     = 78
)

type ProgressFunc func(completed, total int)

type Optimizer struct {
	hasCwebp  bool
	hasMagick bool
}

func New() *Optimizer {
	_, cwebpErr := exec.LookPath("cwebp")
	_, magickErr := exec.LookPath("magick")
	return &Optimizer{
		hasCwebp:  cwebpErr == nil,
		hasMagick: magickErr == nil,
	}
}

func (o *Optimizer) OptimizeChapter(ctx context.Context, chapterDir, optimizedDir string) error {
	return o.OptimizeChapterWithOptions(ctx, chapterDir, optimizedDir, Quality, nil)
}

func (o *Optimizer) OptimizeChapterWithOptions(
	ctx context.Context,
	chapterDir, optimizedDir string,
	quality int,
	onProgress ProgressFunc,
) error {
	if quality < 50 || quality > 95 {
		quality = Quality
	}

	entries, err := os.ReadDir(chapterDir)
	if err != nil {
		return err
	}

	var files []os.DirEntry
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".part") {
			continue
		}
		files = append(files, e)
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name() < files[j].Name()
	})

	if len(files) == 0 {
		return fmt.Errorf("optimizer: no page images found in %s", chapterDir)
	}

	if err := os.MkdirAll(optimizedDir, 0o755); err != nil {
		return err
	}

	var errs []error
	nextIndex := 1

	for i, p := range files {
		if err := ctx.Err(); err != nil {
			return err
		}

		inputPath := filepath.Join(chapterDir, p.Name())
		ext := strings.ToLower(filepath.Ext(inputPath))

		if ext == ".webp" {
			outputPath := filepath.Join(optimizedDir, fmt.Sprintf("%03d.webp", nextIndex))
			if err := copyFile(inputPath, outputPath); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
				slog.Error("optimizer failed to copy webp", "name", p.Name(), "err", err)
			} else {
				nextIndex++
			}
			if onProgress != nil {
				onProgress(i+1, len(files))
			}
			continue
		}

		if !o.hasCwebp && !o.hasMagick {
			return fmt.Errorf("optimizer: neither cwebp nor magick is installed on PATH")
		}

		file, err := os.Open(inputPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s (open): %w", p.Name(), err))
			slog.Error("optimizer failed to open", "name", p.Name(), "err", err)
			if onProgress != nil {
				onProgress(i+1, len(files))
			}
			continue
		}

		imgConfig, _, err := image.DecodeConfig(file)
		file.Close()
		if err != nil {
			// Fallback: encode directly if Go's stdlib decoder doesn't recognize format (e.g., AVIF/BMP)
			outputPath := filepath.Join(optimizedDir, fmt.Sprintf("%03d.webp", nextIndex))
			if encErr := o.encodeWebP(ctx, inputPath, outputPath, quality); encErr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
				slog.Error("optimizer failed to decode/encode", "name", p.Name(), "err", err)
			} else {
				nextIndex++
			}
			if onProgress != nil {
				onProgress(i+1, len(files))
			}
			continue
		}

		if imgConfig.Height > MaxCwebpDim || imgConfig.Width > MaxCwebpDim {
			written, cropErr := o.optimizeOversized(ctx, inputPath, optimizedDir, imgConfig.Width, nextIndex, quality)
			if cropErr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p.Name(), cropErr))
			}
			nextIndex += written
		} else {
			outputPath := filepath.Join(optimizedDir, fmt.Sprintf("%03d.webp", nextIndex))
			if err := o.encodeWebP(ctx, inputPath, outputPath, quality); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
				slog.Error("optimizer failed to encode webp", "name", p.Name(), "err", err)
			} else {
				nextIndex++
			}
		}

		if onProgress != nil {
			onProgress(i+1, len(files))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("chapter optimization finished with %d errors (first: %v)", len(errs), errs[0])
	}
	return nil
}

func (o *Optimizer) optimizeOversized(
	ctx context.Context,
	inputPath, optimizedDir string,
	width, startIndex, quality int,
) (int, error) {
	if !o.hasMagick {
		return 0, fmt.Errorf("magick is required to split oversized image (%dpx)", MaxCwebpDim)
	}

	tmpDir, err := os.MkdirTemp("", "inkfetch_crop_*")
	if err != nil {
		return 0, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpPattern := filepath.Join(tmpDir, "chunk-%03d.jpg")
	cropCmd := exec.CommandContext(ctx,
		"magick", inputPath,
		"-crop", fmt.Sprintf("%dx%d", width, MaxCwebpDim),
		"+repage",
		tmpPattern,
	)

	out, err := cropCmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("crop failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	chunks, err := filepath.Glob(filepath.Join(tmpDir, "chunk-*.jpg"))
	if err != nil || len(chunks) == 0 {
		return 0, fmt.Errorf("no chunks produced")
	}
	sort.Strings(chunks)

	written := 0
	for _, chunkPath := range chunks {
		outputPath := filepath.Join(optimizedDir, fmt.Sprintf("%03d.webp", startIndex+written))
		if err := o.encodeWebP(ctx, chunkPath, outputPath, quality); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

func (o *Optimizer) encodeWebP(ctx context.Context, input, output string, quality int) error {
	if o.hasCwebp {
		cmd := exec.CommandContext(ctx,
			"cwebp",
			"-q", strconv.Itoa(quality),
			"-sharp_yuv",
			"-metadata", "none",
			input,
			"-o", output,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("cwebp failed: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	if o.hasMagick {
		cmd := exec.CommandContext(ctx,
			"magick",
			input,
			"-strip",
			"-quality", strconv.Itoa(quality),
			output,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("magick failed: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	return fmt.Errorf("no webp encoder available")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
