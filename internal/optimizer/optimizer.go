package optimizer

import (
	"context"
	"fmt"
	"image"
	_ "image/gif"
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
	hasCwebp       bool
	hasMagick      bool
	maxDimOverride int
}

type oversizePlan struct {
	resizeWidth  bool
	targetWidth  int
	scaledHeight int
	slices       int
	origSliceH   int
	scaledSliceH int
}

func New() *Optimizer {
	_, cwebpErr := exec.LookPath("cwebp")
	_, magickErr := exec.LookPath("magick")
	return &Optimizer{
		hasCwebp:  cwebpErr == nil,
		hasMagick: magickErr == nil,
	}
}

func (o *Optimizer) maxDim() int {
	if o.maxDimOverride > 0 {
		return o.maxDimOverride
	}
	return MaxCwebpDim
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

		imgConfig, _, probeErr := image.DecodeConfig(file)
		file.Close()

		width, height := imgConfig.Width, imgConfig.Height
		if (probeErr != nil || width <= 0 || height <= 0) && o.hasMagick {
			if w, h, idErr := o.probeDimensionsWithMagick(ctx, inputPath); idErr == nil {
				width, height, probeErr = w, h, nil
			}
		}

		if probeErr != nil || width <= 0 || height <= 0 {
			// Fallback: encode directly if neither stdlib nor magick identify could read dimensions.
			outputPath := filepath.Join(optimizedDir, fmt.Sprintf("%03d.webp", nextIndex))
			if encErr := o.encodeWebP(ctx, inputPath, outputPath, quality); encErr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p.Name(), encErr))
				slog.Error(
					"optimizer failed to decode/encode",
					"name",
					p.Name(),
					"probe_err",
					probeErr,
					"err",
					encErr,
				)
			} else {
				nextIndex++
			}
			if onProgress != nil {
				onProgress(i+1, len(files))
			}
			continue
		}

		limit := o.maxDim()
		if height > limit || width > limit {
			written, cropErr := o.optimizeOversized(
				ctx,
				inputPath,
				optimizedDir,
				width,
				height,
				nextIndex,
				quality,
			)
			if cropErr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p.Name(), cropErr))
				slog.Error(
					"optimizer failed oversized page",
					"name",
					p.Name(),
					"width",
					width,
					"height",
					height,
					"err",
					cropErr,
				)
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
		return fmt.Errorf(
			"chapter optimization finished with %d errors (first: %v)",
			len(errs),
			errs[0],
		)
	}
	return nil
}

func planOversize(width, height, maxDim int) oversizePlan {
	resizeWidth := width > maxDim
	targetWidth := width
	scaledHeight := height
	if resizeWidth {
		targetWidth = maxDim
		scaledHeight = int((int64(height)*int64(maxDim) + int64(width) - 1) / int64(width))
		if scaledHeight < 1 {
			scaledHeight = 1
		}
	}

	slices := 1
	if scaledHeight > maxDim {
		slices = (scaledHeight + maxDim - 1) / maxDim
	}

	origSliceH := (height + slices - 1) / slices
	scaledSliceH := (scaledHeight + slices - 1) / slices

	return oversizePlan{
		resizeWidth:  resizeWidth,
		targetWidth:  targetWidth,
		scaledHeight: scaledHeight,
		slices:       slices,
		origSliceH:   origSliceH,
		scaledSliceH: scaledSliceH,
	}
}

func (o *Optimizer) probeDimensionsWithMagick(
	ctx context.Context,
	inputPath string,
) (int, int, error) {
	cmd := exec.CommandContext(ctx, "magick", "identify", "-ping", "-format", "%w %h\n", inputPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf(
			"magick identify failed: %w (%s)",
			err,
			strings.TrimSpace(string(out)),
		)
	}

	for line := range strings.SplitSeq(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		w, wErr := strconv.Atoi(fields[0])
		h, hErr := strconv.Atoi(fields[1])
		if wErr == nil && hErr == nil && w > 0 && h > 0 {
			return w, h, nil
		}
	}

	return 0, 0, fmt.Errorf(
		"magick identify returned unparseable output: %q",
		strings.TrimSpace(string(out)),
	)
}

func (o *Optimizer) optimizeOversized(
	ctx context.Context,
	inputPath, optimizedDir string,
	width, height, startIndex, quality int,
) (int, error) {
	plan := planOversize(width, height, o.maxDim())

	if o.hasCwebp {
		written, cwebpErr := o.optimizeOversizedWithCwebp(
			ctx,
			inputPath,
			optimizedDir,
			width,
			height,
			startIndex,
			quality,
			plan,
		)
		if cwebpErr == nil {
			return written, nil
		}
		if ctx.Err() != nil || !o.hasMagick {
			return 0, cwebpErr
		}

		written, magickErr := o.optimizeOversizedWithMagick(
			ctx,
			inputPath,
			optimizedDir,
			startIndex,
			quality,
			plan,
		)
		if magickErr == nil {
			return written, nil
		}
		return 0, fmt.Errorf("oversized encode failed (cwebp: %v; magick: %w)", cwebpErr, magickErr)
	}

	if o.hasMagick {
		return o.optimizeOversizedWithMagick(
			ctx,
			inputPath,
			optimizedDir,
			startIndex,
			quality,
			plan,
		)
	}

	return 0, fmt.Errorf("no webp encoder available")
}

func (o *Optimizer) optimizeOversizedWithCwebp(
	ctx context.Context,
	inputPath, optimizedDir string,
	width, height, startIndex, quality int,
	plan oversizePlan,
) (int, error) {
	if plan.slices == 1 {
		outputPath := filepath.Join(optimizedDir, fmt.Sprintf("%03d.webp", startIndex))
		extraArgs := []string{"-resize", strconv.Itoa(plan.targetWidth), "0"}
		if err := o.runCwebp(ctx, extraArgs, inputPath, outputPath, quality); err != nil {
			return 0, err
		}
		return 1, nil
	}

	writtenPaths := make([]string, 0, plan.slices)
	written := 0
	for y := 0; y < height; y += plan.origSliceH {
		if err := ctx.Err(); err != nil {
			removeFiles(writtenPaths)
			return 0, err
		}

		h := min(plan.origSliceH, height-y)
		extraArgs := []string{"-crop", "0", strconv.Itoa(y), strconv.Itoa(width), strconv.Itoa(h)}
		if plan.resizeWidth {
			extraArgs = append(extraArgs, "-resize", strconv.Itoa(plan.targetWidth), "0")
		}

		outputPath := filepath.Join(optimizedDir, fmt.Sprintf("%03d.webp", startIndex+written))
		if err := o.runCwebp(ctx, extraArgs, inputPath, outputPath, quality); err != nil {
			removeFiles(writtenPaths)
			return 0, err
		}
		writtenPaths = append(writtenPaths, outputPath)
		written++
	}
	return written, nil
}

func (o *Optimizer) optimizeOversizedWithMagick(
	ctx context.Context,
	inputPath, optimizedDir string,
	startIndex, quality int,
	plan oversizePlan,
) (int, error) {
	if plan.slices == 1 {
		outputPath := filepath.Join(optimizedDir, fmt.Sprintf("%03d.webp", startIndex))
		extraArgs := []string{"-resize", fmt.Sprintf("%dx>", plan.targetWidth)}
		if err := o.runMagickWebP(ctx, extraArgs, inputPath, outputPath, quality); err != nil {
			return 0, err
		}
		return 1, nil
	}

	tmpDir, err := os.MkdirTemp("", "inkfetch_crop_*")
	if err != nil {
		return 0, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpPattern := filepath.Join(tmpDir, "slice-%06d.webp")
	// +repage before resize/crop clears virtual canvas offsets (e.g. PNG oFFs); +repage after strips tile offsets.
	args := []string{inputPath, "-delete", "1--1", "+repage"}
	if plan.resizeWidth {
		args = append(args, "-resize", fmt.Sprintf("%dx>", plan.targetWidth))
	}
	args = append(args,
		"-crop", fmt.Sprintf("%dx%d", plan.targetWidth, plan.scaledSliceH),
		"+repage",
		"-strip",
		"-quality", strconv.Itoa(quality),
		tmpPattern,
	)

	out, err := exec.CommandContext(ctx, "magick", args...).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("magick crop failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	chunks, err := filepath.Glob(filepath.Join(tmpDir, "slice-*.webp"))
	if err != nil || len(chunks) == 0 {
		return 0, fmt.Errorf("no chunks produced")
	}
	sort.Strings(chunks)

	movedPaths := make([]string, 0, len(chunks))
	written := 0
	for _, chunkPath := range chunks {
		outputPath := filepath.Join(optimizedDir, fmt.Sprintf("%03d.webp", startIndex+written))
		if err := moveFile(chunkPath, outputPath); err != nil {
			removeFiles(movedPaths)
			return 0, err
		}
		movedPaths = append(movedPaths, outputPath)
		written++
	}
	return written, nil
}

func (o *Optimizer) encodeWebP(ctx context.Context, input, output string, quality int) error {
	if o.hasCwebp {
		cwebpErr := o.runCwebp(ctx, nil, input, output, quality)
		if cwebpErr == nil {
			return nil
		}
		if ctx.Err() != nil || !o.hasMagick {
			return cwebpErr
		}

		magickErr := o.runMagickWebP(ctx, nil, input, output, quality)
		if magickErr == nil {
			return nil
		}
		return fmt.Errorf("webp encode failed (cwebp: %v; magick: %w)", cwebpErr, magickErr)
	}

	if o.hasMagick {
		return o.runMagickWebP(ctx, nil, input, output, quality)
	}

	return fmt.Errorf("no webp encoder available")
}

func (o *Optimizer) runCwebp(
	ctx context.Context,
	extraArgs []string,
	input, output string,
	quality int,
) error {
	baseArgs := make([]string, 0, 8+len(extraArgs))
	baseArgs = append(baseArgs, "-q", strconv.Itoa(quality), "-sharp_yuv", "-metadata", "none")
	baseArgs = append(baseArgs, extraArgs...)

	args := append(append([]string(nil), baseArgs...), input, "-o", output)
	out, err := exec.CommandContext(ctx, "cwebp", args...).CombinedOutput()
	if err != nil && ctx.Err() == nil && isPartitionOverflow(out) {
		retryArgs := append(
			append([]string(nil), baseArgs...),
			"-partition_limit",
			"100",
			input,
			"-o",
			output,
		)
		out, err = exec.CommandContext(ctx, "cwebp", retryArgs...).CombinedOutput()
	}
	if err != nil {
		_ = os.Remove(output)
		return fmt.Errorf("cwebp failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (o *Optimizer) runMagickWebP(
	ctx context.Context,
	extraArgs []string,
	input, output string,
	quality int,
) error {
	args := make([]string, 0, 8+len(extraArgs))
	args = append(args, input, "-delete", "1--1", "+repage")
	args = append(args, extraArgs...)
	args = append(args, "-strip", "-quality", strconv.Itoa(quality), output)

	out, err := exec.CommandContext(ctx, "magick", args...).CombinedOutput()
	if err != nil {
		_ = os.Remove(output)
		return fmt.Errorf("magick failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func isPartitionOverflow(out []byte) bool {
	s := string(out)
	return strings.Contains(s, "PARTITION0_OVERFLOW") || strings.Contains(s, "Partition0 overflow")
}

func removeFiles(paths []string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	_ = os.Remove(src)
	return nil
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
