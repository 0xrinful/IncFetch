package optimizer

import (
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestPlanOversize(t *testing.T) {
	tests := []struct {
		name         string
		width        int
		height       int
		maxDim       int
		wantResize   bool
		wantTargetW  int
		wantScaledH  int
		wantSlices   int
		wantOrigH    int
		wantScaledSH int
	}{
		{
			name:         "tall barely over",
			width:        800,
			height:       16381,
			maxDim:       16380,
			wantResize:   false,
			wantTargetW:  800,
			wantScaledH:  16381,
			wantSlices:   2,
			wantOrigH:    8191,
			wantScaledSH: 8191,
		},
		{
			name:         "tall exact multiple",
			width:        800,
			height:       32760,
			maxDim:       16380,
			wantResize:   false,
			wantTargetW:  800,
			wantScaledH:  32760,
			wantSlices:   2,
			wantOrigH:    16380,
			wantScaledSH: 16380,
		},
		{
			name:         "super tall",
			width:        800,
			height:       70000,
			maxDim:       16380,
			wantResize:   false,
			wantTargetW:  800,
			wantScaledH:  70000,
			wantSlices:   5,
			wantOrigH:    14000,
			wantScaledSH: 14000,
		},
		{
			name:         "wide only",
			width:        20000,
			height:       12000,
			maxDim:       16380,
			wantResize:   true,
			wantTargetW:  16380,
			wantScaledH:  9828,
			wantSlices:   1,
			wantOrigH:    12000,
			wantScaledSH: 9828,
		},
		{
			name:         "wide and tall fits after width scale",
			width:        20000,
			height:       18000,
			maxDim:       16380,
			wantResize:   true,
			wantTargetW:  16380,
			wantScaledH:  14742,
			wantSlices:   1,
			wantOrigH:    18000,
			wantScaledSH: 14742,
		},
		{
			name:         "wide and tall still tall after width scale",
			width:        20000,
			height:       50000,
			maxDim:       16380,
			wantResize:   true,
			wantTargetW:  16380,
			wantScaledH:  40950,
			wantSlices:   3,
			wantOrigH:    16667,
			wantScaledSH: 13650,
		},
		{
			name:         "extreme banner clamps height to at least 1",
			width:        100000,
			height:       1,
			maxDim:       16380,
			wantResize:   true,
			wantTargetW:  16380,
			wantScaledH:  1,
			wantSlices:   1,
			wantOrigH:    1,
			wantScaledSH: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planOversize(tc.width, tc.height, tc.maxDim)
			if got.resizeWidth != tc.wantResize ||
				got.targetWidth != tc.wantTargetW ||
				got.scaledHeight != tc.wantScaledH ||
				got.slices != tc.wantSlices ||
				got.origSliceH != tc.wantOrigH ||
				got.scaledSliceH != tc.wantScaledSH {
				t.Fatalf("planOversize(%d, %d, %d) = %+v, want resize=%v targetW=%d scaledH=%d slices=%d origH=%d scaledSH=%d",
					tc.width, tc.height, tc.maxDim, got,
					tc.wantResize, tc.wantTargetW, tc.wantScaledH, tc.wantSlices, tc.wantOrigH, tc.wantScaledSH)
			}

			if got.targetWidth < 1 || got.targetWidth > tc.maxDim {
				t.Fatalf("targetWidth out of bounds: %d (maxDim=%d)", got.targetWidth, tc.maxDim)
			}
			if got.scaledSliceH < 1 || got.scaledSliceH > tc.maxDim {
				t.Fatalf("scaledSliceH out of bounds: %d (maxDim=%d)", got.scaledSliceH, tc.maxDim)
			}
			if got.slices < 1 {
				t.Fatalf("slices must be >= 1, got %d", got.slices)
			}
		})
	}
}

func requireTools(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("cwebp"); err != nil {
		t.Skip("cwebp not installed on PATH")
	}
	if _, err := exec.LookPath("magick"); err != nil {
		t.Skip("magick not installed on PATH")
	}
}

func solidImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	c := color.RGBA{R: 220, G: 225, B: 230, A: 255}
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i] = c.R
		img.Pix[i+1] = c.G
		img.Pix[i+2] = c.B
		img.Pix[i+3] = c.A
	}
	return img
}

func writeTestPNG(t *testing.T, path string, w, h int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, solidImage(w, h)); err != nil {
		t.Fatal(err)
	}
}

func writeTestJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, solidImage(w, h), &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
}

func writeTestWebP(t *testing.T, path string, w, h int) {
	t.Helper()
	tmpPNG := path + ".src.png"
	writeTestPNG(t, tmpPNG, w, h)
	defer os.Remove(tmpPNG)
	out, err := exec.Command("cwebp", "-q", "75", tmpPNG, "-o", path).CombinedOutput()
	if err != nil {
		t.Fatalf("create test webp: %v (%s)", err, string(out))
	}
}

func writeTestMultiFrameGIF(t *testing.T, path string, w, h, frames int) {
	t.Helper()
	palette := color.Palette{color.White, color.Black}
	anim := &gif.GIF{}
	for f := range frames {
		frame := image.NewPaletted(image.Rect(0, 0, w, h), palette)
		idx := uint8(f % len(palette))
		for i := range frame.Pix {
			frame.Pix[i] = idx
		}
		anim.Image = append(anim.Image, frame)
		anim.Delay = append(anim.Delay, 10)
	}
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := gif.EncodeAll(out, anim); err != nil {
		t.Fatal(err)
	}
}

func listWebPOutputs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

func readWebPDims(t *testing.T, path string) (int, int) {
	t.Helper()
	out, err := exec.Command("magick", "identify", "-ping", "-format", "%w %h\n", path).CombinedOutput()
	if err != nil {
		t.Fatalf("identify %s: %v (%s)", path, err, string(out))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected single frame in %s, got %d lines (%q)", path, len(lines), string(out))
	}
	fields := strings.Fields(lines[0])
	if len(fields) != 2 {
		t.Fatalf("unexpected identify output for %s: %q", path, string(out))
	}
	w, err1 := strconv.Atoi(fields[0])
	h, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		t.Fatalf("parse dims %q: %v %v", string(out), err1, err2)
	}
	return w, h
}

func TestOptimizeChapter_NormalMixed(t *testing.T) {
	requireTools(t)

	rawDir := t.TempDir()
	optDir := filepath.Join(t.TempDir(), "optimized")

	writeTestPNG(t, filepath.Join(rawDir, "001.png"), 64, 100)
	writeTestJPEG(t, filepath.Join(rawDir, "002.jpg"), 64, 120)
	writeTestWebP(t, filepath.Join(rawDir, "003.webp"), 64, 80)
	writeTestPNG(t, filepath.Join(rawDir, "004.jpg.part"), 64, 100)

	var progressCalls [][2]int
	opt := New()
	err := opt.OptimizeChapterWithOptions(context.Background(), rawDir, optDir, Quality, func(completed, total int) {
		progressCalls = append(progressCalls, [2]int{completed, total})
	})
	if err != nil {
		t.Fatalf("OptimizeChapterWithOptions failed: %v", err)
	}

	got := listWebPOutputs(t, optDir)
	want := []string{"001.webp", "002.webp", "003.webp"}
	if len(got) != len(want) {
		t.Fatalf("outputs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("outputs = %v, want %v", got, want)
		}
	}

	wantProg := [][2]int{{1, 3}, {2, 3}, {3, 3}}
	if len(progressCalls) != len(wantProg) {
		t.Fatalf("progressCalls = %v, want %v", progressCalls, wantProg)
	}
	for i := range wantProg {
		if progressCalls[i] != wantProg[i] {
			t.Fatalf("progressCalls = %v, want %v", progressCalls, wantProg)
		}
	}
}

func TestOptimizeChapter_TallVerticalStrip(t *testing.T) {
	requireTools(t)

	backends := []struct {
		name string
		opt  *Optimizer
	}{
		{name: "cwebp", opt: &Optimizer{hasCwebp: true}},
		{name: "magick", opt: &Optimizer{hasMagick: true}},
	}

	formats := []struct {
		name  string
		write func(t *testing.T, path string, w, h int)
		ext   string
	}{
		{name: "png", write: writeTestPNG, ext: ".png"},
		{name: "jpeg", write: writeTestJPEG, ext: ".jpg"},
	}

	for _, b := range backends {
		for _, f := range formats {
			t.Run(b.name+"/"+f.name, func(t *testing.T) {
				rawDir := t.TempDir()
				optDir := filepath.Join(t.TempDir(), "optimized")

				writeTestPNG(t, filepath.Join(rawDir, "001.png"), 64, 100)
				f.write(t, filepath.Join(rawDir, "002"+f.ext), 64, 17000)
				writeTestJPEG(t, filepath.Join(rawDir, "003.jpg"), 64, 100)

				if err := b.opt.OptimizeChapter(context.Background(), rawDir, optDir); err != nil {
					t.Fatalf("OptimizeChapter failed: %v", err)
				}

				got := listWebPOutputs(t, optDir)
				want := []string{"001.webp", "002.webp", "003.webp", "004.webp"}
				if len(got) != len(want) {
					t.Fatalf("outputs = %v, want %v", got, want)
				}

				wantDims := [][2]int{{64, 100}, {64, 8500}, {64, 8500}, {64, 100}}
				for i, name := range want {
					if got[i] != name {
						t.Fatalf("outputs = %v, want %v", got, want)
					}
					w, h := readWebPDims(t, filepath.Join(optDir, name))
					if w != wantDims[i][0] || h != wantDims[i][1] {
						t.Fatalf("%s dims = %dx%d, want %dx%d", name, w, h, wantDims[i][0], wantDims[i][1])
					}
				}
			})
		}
	}
}

func TestOptimizeChapter_OversizedWidth(t *testing.T) {
	requireTools(t)

	backends := []struct {
		name string
		opt  *Optimizer
	}{
		{name: "cwebp", opt: &Optimizer{hasCwebp: true}},
		{name: "magick", opt: &Optimizer{hasMagick: true}},
	}

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			rawDir := t.TempDir()
			optDir := filepath.Join(t.TempDir(), "optimized")

			writeTestPNG(t, filepath.Join(rawDir, "001.png"), 17000, 64)

			if err := b.opt.OptimizeChapter(context.Background(), rawDir, optDir); err != nil {
				t.Fatalf("OptimizeChapter failed: %v", err)
			}

			got := listWebPOutputs(t, optDir)
			if len(got) != 1 || got[0] != "001.webp" {
				t.Fatalf("outputs = %v, want [001.webp]", got)
			}

			w, h := readWebPDims(t, filepath.Join(optDir, "001.webp"))
			if w != MaxCwebpDim || h < 1 || h > 64 {
				t.Fatalf("001.webp dims = %dx%d, want width=%d and 1<=height<=64", w, h, MaxCwebpDim)
			}
		})
	}
}

func TestOptimizeChapter_WideAndTall(t *testing.T) {
	requireTools(t)

	backends := []struct {
		name string
		opt  *Optimizer
	}{
		{name: "cwebp", opt: &Optimizer{hasCwebp: true, maxDimOverride: 200}},
		{name: "magick", opt: &Optimizer{hasMagick: true, maxDimOverride: 200}},
	}

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			rawDir := t.TempDir()
			optDir := filepath.Join(t.TempDir(), "optimized")

			writeTestPNG(t, filepath.Join(rawDir, "001.png"), 400, 900)

			if err := b.opt.OptimizeChapter(context.Background(), rawDir, optDir); err != nil {
				t.Fatalf("OptimizeChapter failed: %v", err)
			}

			got := listWebPOutputs(t, optDir)
			want := []string{"001.webp", "002.webp", "003.webp"}
			if len(got) != len(want) {
				t.Fatalf("outputs = %v, want %v", got, want)
			}

			totalH := 0
			for i, name := range want {
				if got[i] != name {
					t.Fatalf("outputs = %v, want %v", got, want)
				}
				w, h := readWebPDims(t, filepath.Join(optDir, name))
				if w != 200 || h < 1 || h > 200 {
					t.Fatalf("%s dims = %dx%d, want width=200 and 1<=height<=200", name, w, h)
				}
				totalH += h
			}
			if totalH != 450 {
				t.Fatalf("total scaled height = %d, want 450", totalH)
			}
		})
	}
}

func TestOptimizeChapter_OversizedAVIF(t *testing.T) {
	requireTools(t)

	rawDir := t.TempDir()
	optDir := filepath.Join(t.TempDir(), "optimized")

	avifPath := filepath.Join(rawDir, "001.avif")
	out, err := exec.Command("magick", "-size", "64x500", "xc:white", avifPath).CombinedOutput()
	if err != nil {
		t.Fatalf("generate test avif: %v (%s)", err, string(out))
	}

	opt := &Optimizer{hasCwebp: true, hasMagick: true, maxDimOverride: 200}
	if err := opt.OptimizeChapter(context.Background(), rawDir, optDir); err != nil {
		t.Fatalf("OptimizeChapter failed: %v", err)
	}

	got := listWebPOutputs(t, optDir)
	want := []string{"001.webp", "002.webp", "003.webp"}
	if len(got) != len(want) {
		t.Fatalf("outputs = %v, want %v", got, want)
	}

	wantDims := [][2]int{{64, 167}, {64, 167}, {64, 166}}
	for i, name := range want {
		if got[i] != name {
			t.Fatalf("outputs = %v, want %v", got, want)
		}
		w, h := readWebPDims(t, filepath.Join(optDir, name))
		if w != wantDims[i][0] || h != wantDims[i][1] {
			t.Fatalf("%s dims = %dx%d, want %dx%d", name, w, h, wantDims[i][0], wantDims[i][1])
		}
	}
}

func TestOptimizeChapter_MultiFrameTallGIF(t *testing.T) {
	requireTools(t)

	rawDir := t.TempDir()
	optDir := filepath.Join(t.TempDir(), "optimized")

	writeTestMultiFrameGIF(t, filepath.Join(rawDir, "001.gif"), 64, 500, 2)

	opt := &Optimizer{hasCwebp: true, hasMagick: true, maxDimOverride: 200}
	if err := opt.OptimizeChapter(context.Background(), rawDir, optDir); err != nil {
		t.Fatalf("OptimizeChapter failed: %v", err)
	}

	got := listWebPOutputs(t, optDir)
	want := []string{"001.webp", "002.webp", "003.webp"}
	if len(got) != len(want) {
		t.Fatalf("outputs = %v, want %v", got, want)
	}

	wantDims := [][2]int{{64, 167}, {64, 167}, {64, 166}}
	for i, name := range want {
		if got[i] != name {
			t.Fatalf("outputs = %v, want %v", got, want)
		}
		w, h := readWebPDims(t, filepath.Join(optDir, name))
		if w != wantDims[i][0] || h != wantDims[i][1] {
			t.Fatalf("%s dims = %dx%d, want %dx%d", name, w, h, wantDims[i][0], wantDims[i][1])
		}
	}
}

func TestOptimizeChapter_CorruptImageReportsEncoderError(t *testing.T) {
	requireTools(t)

	rawDir := t.TempDir()
	optDir := filepath.Join(t.TempDir(), "optimized")

	if err := os.WriteFile(filepath.Join(rawDir, "001.jpg"), []byte("not-an-image"), 0o644); err != nil {
		t.Fatal(err)
	}

	opt := New()
	err := opt.OptimizeChapter(context.Background(), rawDir, optDir)
	if err == nil {
		t.Fatal("expected error for corrupt image, got nil")
	}
	if strings.Contains(err.Error(), "image: unknown format") {
		t.Fatalf("expected encoder error, got masked DecodeConfig error: %v", err)
	}
	if !strings.Contains(err.Error(), "failed") {
		t.Fatalf("expected encoder failure message, got: %v", err)
	}

	if got := listWebPOutputs(t, optDir); len(got) != 0 {
		t.Fatalf("expected no output files on failure, got %v", got)
	}
}
