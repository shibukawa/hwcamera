// Command npu runs an image model on the NPU (hwnpu) over camera frames:
// each frame becomes the model's input tensor (tensor package) and the
// output's top scores are printed.
//
//	go run ./npu -model mobilenet_v3.mlpackage            # macOS, Apple Neural Engine
//	go run ./npu -model mobilenet.xml -labels labels.txt  # Intel Core Ultra (OpenVINO IR)
//
// The input layout (NCHW or NHWC) and size come from the model; the
// normalisation defaults to ImageNet's and can be changed with -norm.
package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/shibukawa/hwcamera"
	"github.com/shibukawa/hwcamera/tensor"
	"github.com/shibukawa/hwnpu"
)

func main() {
	model := flag.String("model", "", "model: .mlpackage / .mlmodelc (Core ML) or .xml (OpenVINO IR)")
	labelsPath := flag.String("labels", "", "text file with one class name per line")
	device := flag.String("device", "", "camera ID (hwcamera list) or a kind (front, back, back+ultrawide, ...); empty for the default camera")
	norm := flag.String("norm", "imagenet", "input normalisation: imagenet, unit (0..1), symmetric (-1..1), raw (0..255)")
	bgr := flag.Bool("bgr", false, "the model takes BGR")
	top := flag.Int("top", 3, "scores to print per frame")
	flag.Parse()
	if *model == "" {
		flag.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	dev, err := hwnpu.Open(ctx, hwnpu.WithPowerPolicy(hwnpu.PowerLow))
	if err != nil {
		log.Fatal(err)
	}
	defer dev.Close()
	gr, err := dev.Graph()
	if err != nil {
		log.Fatal(err)
	}
	src := hwnpu.GraphSource{Path: *model}
	if strings.HasSuffix(*model, ".xml") {
		xml, err := os.ReadFile(*model)
		if err != nil {
			log.Fatal(err)
		}
		bin, err := os.ReadFile(strings.TrimSuffix(*model, ".xml") + ".bin")
		if err != nil {
			log.Fatal(err)
		}
		src = hwnpu.GraphSource{Format: hwnpu.GraphFormatOpenVINOIR, XML: xml, Bin: bin}
	}
	g, err := gr.Compile(ctx, src)
	if err != nil {
		log.Fatal(err)
	}
	defer g.Close()

	var in, out hwnpu.ArgInfo
	for _, a := range g.Args() {
		if a.Kind == hwnpu.ArgInput && in.Bytes == 0 {
			in = a
		}
		if a.Kind == hwnpu.ArgOutput && out.Bytes == 0 {
			out = a
		}
	}
	spec, err := tensor.FromShape(in.Dims)
	if err != nil {
		log.Fatal(err)
	}
	switch in.DType {
	case hwnpu.F16:
		spec.DType = tensor.Float16
	case hwnpu.F32:
		spec.DType = tensor.Float32
	case hwnpu.U8:
		spec.DType = tensor.Uint8
	case hwnpu.I8:
		spec.DType = tensor.Int8
	default:
		log.Fatalf("input %s has element type %v", in.Name, in.DType)
	}
	switch *norm {
	case "imagenet":
		spec.Mean, spec.Std = tensor.ImageNetMean, tensor.ImageNetStd
	case "symmetric":
		spec.Mean, spec.Std = tensor.SymmetricMean, tensor.SymmetricStd
	case "raw":
		spec.Scale = 255
	}
	if *bgr {
		spec.Order = tensor.BGR
	}
	spec.Resize = tensor.CenterCrop
	if spec.Bytes() != in.Bytes {
		log.Fatalf("input %s: %d bytes, tensor spec gives %d", in.Name, in.Bytes, spec.Bytes())
	}
	labels := readLabels(*labelsPath)

	inBuf, err := dev.AllocBuffer(in.Bytes)
	if err != nil {
		log.Fatal(err)
	}
	defer inBuf.Close()
	outBuf, err := dev.AllocBuffer(out.Bytes)
	if err != nil {
		log.Fatal(err)
	}
	defer outBuf.Close()
	req, err := g.NewRequest()
	if err != nil {
		log.Fatal(err)
	}
	defer req.Close()
	if err := req.Bind(in.Index, inBuf); err != nil {
		log.Fatal(err)
	}
	if err := req.Bind(out.Index, outBuf); err != nil {
		log.Fatal(err)
	}

	// A model takes a few hundred pixels: Low runs the camera at about
	// 640x480, which tensor.Fill scales down cheaply. RGBA from the camera
	// saves a YUV conversion inside tensor.Fill.
	id, kind := *device, hwcamera.Low
	if k, err := hwcamera.ParseKind(id); err == nil { // -device back, front+high, ...
		id = ""
		if k&(hwcamera.Low|hwcamera.Mid|hwcamera.High) != 0 {
			kind = 0 // the flag chose the resolution
		}
		kind |= k
	}
	opts := []hwcamera.Option{hwcamera.WithKind(kind), hwcamera.WithFormat(hwcamera.RGBA)}
	cam, err := hwcamera.Open(ctx, id, opts...)
	if err != nil {
		log.Fatal(err)
	}
	defer cam.Close()
	fmt.Printf("camera %s (%s) -> %s %v %s on %s\n", cam.Info().Name, cam.Mode(), in.Name, in.Dims, in.DType, dev.Info().Name)

	for ctx.Err() == nil {
		f, err := cam.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Fatal(err)
		}
		t0 := time.Now()
		_, err = tensor.Fill(inBuf.Bytes(), f, spec)
		f.Release()
		if err != nil {
			log.Fatal(err)
		}
		inBuf.SyncToDevice(0, in.Bytes)
		t1 := time.Now()
		if err := req.Run(ctx); err != nil {
			log.Fatal(err)
		}
		outBuf.SyncFromDevice(0, out.Bytes)
		t2 := time.Now()
		scores := decode(outBuf.Bytes(), out.DType)
		fmt.Printf("prep %5.1fms  npu %5.1fms  %s\n", ms(t1.Sub(t0)), ms(t2.Sub(t1)), topK(scores, labels, *top))
	}
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func decode(b []byte, dt hwnpu.DType) []float32 {
	switch dt {
	case hwnpu.F16:
		v := make([]float32, len(b)/2)
		for i := range v {
			v[i] = tensor.F16ToF32(binary.NativeEndian.Uint16(b[2*i:]))
		}
		return v
	case hwnpu.F32:
		v := make([]float32, len(b)/4)
		for i := range v {
			v[i] = math.Float32frombits(binary.NativeEndian.Uint32(b[4*i:]))
		}
		return v
	}
	log.Fatalf("output element type %v", dt)
	return nil
}

func topK(s []float32, labels []string, k int) string {
	idx := make([]int, len(s))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return s[idx[a]] > s[idx[b]] })
	var parts []string
	for _, i := range idx[:min(k, len(idx))] {
		name := fmt.Sprint(i)
		if i < len(labels) {
			name = labels[i]
		}
		parts = append(parts, fmt.Sprintf("%s %.3f", name, s[i]))
	}
	return strings.Join(parts, " | ")
}

func readLabels(path string) []string {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, strings.TrimSpace(sc.Text()))
	}
	return out
}
