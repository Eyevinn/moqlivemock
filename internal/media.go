package internal

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
)

type AVCData struct {
	inInit  *mp4.InitSegment
	outInit *mp4.InitSegment
	Spss    [][]byte
	Ppss    [][]byte
	codec   string
	width   uint32
	height  uint32
}

// initAVCData initializes AVCData from an init segment and samples.
// It first checks the sample entry in the init segment for SPS and PPS nalus.
// Then it processes each sample to extract SPS and PPS nalus.
func initAVCData(init *mp4.InitSegment, samples []mp4.FullSample) (*AVCData, error) {
	ad := &AVCData{
		inInit: init,
	}
	trak := init.Moov.Trak
	avcX := trak.Mdia.Minf.Stbl.Stsd.AvcX
	sampleEntry := avcX.Type()
	if sampleEntry == "avc1" {
		ad.Spss = avcX.AvcC.SPSnalus
		ad.Ppss = avcX.AvcC.PPSnalus
	}
	work := make([]byte, 4)
	for i := range samples {
		rawData := samples[i].Data
		nalus, err := avc.GetNalusFromSample(rawData)
		if err != nil {
			return nil, fmt.Errorf("could not get nalus from sample: %w", err)
		}
		samples[i].Data = samples[i].Data[:0]
		for _, nalu := range nalus {
			switch avc.GetNaluType(nalu[0]) {
			case avc.NALU_SPS:
				ad.Spss = appendNewNALU(ad.Spss, nalu)
			case avc.NALU_PPS:
				ad.Ppss = appendNewNALU(ad.Ppss, nalu)
			case avc.NALU_IDR, avc.NALU_NON_IDR, avc.NALU_SEI:
				binary.BigEndian.PutUint32(work, uint32(len(nalu)))
				samples[i].Data = append(samples[i].Data, work...)
				samples[i].Data = append(samples[i].Data, nalu...)
			default:
				// silently drop other NALU types to make the samples smaller
			}
		}
	}
	if len(ad.Spss) != 1 || len(ad.Ppss) != 1 {
		return nil, fmt.Errorf("not exactly one SPS and PPS nalus found")
	}
	for i := range samples {
		if avc.GetNaluType(samples[i].Data[4]) == avc.NALU_IDR {
			// Insert SPS and PPS
			totSize := 4 + len(ad.Spss[0]) + 4 + len(ad.Ppss[0]) + len(samples[i].Data)
			newData := make([]byte, 0, totSize)
			binary.BigEndian.PutUint32(work, uint32(len(ad.Spss[0])))
			newData = append(newData, work...)
			newData = append(newData, ad.Spss[0]...)
			binary.BigEndian.PutUint32(work, uint32(len(ad.Ppss[0])))
			newData = append(newData, work...)
			newData = append(newData, ad.Ppss[0]...)
			newData = append(newData, samples[i].Data...)
			samples[i].Data = newData
		}
	}

	// Generate an output init segment with avc3 sample descriptor
	ad.outInit = mp4.CreateEmptyInit()
	timeScale := trak.Mdia.Mdhd.Timescale
	ad.outInit.AddEmptyTrack(timeScale, "video", "und")
	err := ad.outInit.Moov.Trak.SetAVCDescriptor("avc3", ad.Spss, ad.Ppss, false)
	if err != nil {
		return nil, fmt.Errorf("could not set AVC descriptor: %w", err)
	}
	sps, err := avc.ParseSPSNALUnit(ad.Spss[0], false)
	if err != nil {
		return nil, fmt.Errorf("could not decode SPS: %w", err)
	}
	ad.codec = avc.CodecString("avc3", sps)
	ad.width = uint32(sps.Width)
	ad.height = uint32(sps.Height)
	return ad, nil
}

// appendNewNALU appends a NALU to the list if it is not already present.
func appendNewNALU(nalus [][]byte, nalu []byte) [][]byte {
	for _, v := range nalus {
		if bytes.Equal(v, nalu) {
			return nalus
		}
	}
	return append(nalus, nalu)
}

// GenCMAFInitData returns a base64 encoded CMAF initialization segment.
func (d *AVCData) GenCMAFInitData() ([]byte, error) {
	sw := bits.NewFixedSliceWriter(int(d.outInit.Size()))
	err := d.outInit.EncodeSW(sw)
	if err != nil {
		return nil, err
	}
	return sw.Bytes(), nil
}

func (d *AVCData) Codec() string {
	return d.codec
}

type HEVCData struct {
	inInit  *mp4.InitSegment
	outInit *mp4.InitSegment
	Vpss    [][]byte
	Spss    [][]byte
	Ppss    [][]byte
	codec   string
	width   uint32
	height  uint32
}

// initHEVCData initializes HEVCData from an init segment and samples.
func initHEVCData(init *mp4.InitSegment, samples []mp4.FullSample) (*HEVCData, error) {
	hd := &HEVCData{inInit: init}

	trak := init.Moov.Trak
	hvcX := trak.Mdia.Minf.Stbl.Stsd.HvcX
	if hvcX == nil || hvcX.HvcC == nil {
		return nil, fmt.Errorf("no hvcC box found")
	}

	// Extract VPS / SPS / PPS from hvcC NaluArrays
	for _, arr := range hvcX.HvcC.NaluArrays {
		switch arr.NaluType() {
		case hevc.NALU_VPS:
			hd.Vpss = append(hd.Vpss, arr.Nalus...)
		case hevc.NALU_SPS:
			hd.Spss = append(hd.Spss, arr.Nalus...)
		case hevc.NALU_PPS:
			hd.Ppss = append(hd.Ppss, arr.Nalus...)
		}
	}

	if len(hd.Spss) == 0 || len(hd.Ppss) == 0 {
		return nil, fmt.Errorf("missing SPS or PPS in hvcC")
	}

	work := make([]byte, 4)

	// Rewrite samples: parse length-prefixed NALUs
	for i := range samples {
		data := samples[i].Data
		samples[i].Data = samples[i].Data[:0]

		for len(data) >= 4 {
			naluLen := binary.BigEndian.Uint32(data[:4])
			data = data[4:]

			if int(naluLen) > len(data) {
				return nil, fmt.Errorf("invalid HEVC NALU length")
			}

			nalu := data[:naluLen]
			data = data[naluLen:]

			naluType := hevc.GetNaluType(nalu[0])

			switch naluType {
			case hevc.NALU_VPS:
				hd.Vpss = appendNewNALU(hd.Vpss, nalu)
			case hevc.NALU_SPS:
				hd.Spss = appendNewNALU(hd.Spss, nalu)
			case hevc.NALU_PPS:
				hd.Ppss = appendNewNALU(hd.Ppss, nalu)
			default:
				binary.BigEndian.PutUint32(work, uint32(len(nalu)))
				samples[i].Data = append(samples[i].Data, work...)
				samples[i].Data = append(samples[i].Data, nalu...)
			}
		}
	}

	// Insert VPS/SPS/PPS before IRAP samples (NALU type 16–23)
	for i := range samples {
		if len(samples[i].Data) < 5 {
			continue
		}

		if hevc.IsRAPSample(samples[i].Data) {
			newData := make([]byte, 0)

			for _, ps := range [][]byte{
				hd.Vpss[0],
				hd.Spss[0],
				hd.Ppss[0],
			} {
				binary.BigEndian.PutUint32(work, uint32(len(ps)))
				newData = append(newData, work...)
				newData = append(newData, ps...)
			}

			newData = append(newData, samples[i].Data...)
			samples[i].Data = newData
		}
	}

	// Create CMAF-compliant init segment (hev1)
	hd.outInit = mp4.CreateEmptyInit()
	timeScale := trak.Mdia.Mdhd.Timescale
	hd.outInit.AddEmptyTrack(timeScale, "video", "und")

	err := hd.outInit.Moov.Trak.SetHEVCDescriptor(
		"hev1",
		hd.Vpss,
		hd.Spss,
		hd.Ppss,
		nil,   // DCI
		false, // includePS
	)
	if err != nil {
		return nil, fmt.Errorf("could not set HEVC descriptor: %w", err)
	}

	// Parse SPS for codec string and resolution
	sps, err := hevc.ParseSPSNALUnit(hd.Spss[0])
	if err != nil {
		return nil, fmt.Errorf("could not parse HEVC SPS: %w", err)
	}

	hd.codec = hevc.CodecString("hev1", sps)
	hd.width = uint32(sps.PicWidthInLumaSamples)
	hd.height = uint32(sps.PicHeightInLumaSamples)

	return hd, nil
}

// GenCMAFInitData returns the CMAF init segment.
func (d *HEVCData) GenCMAFInitData() ([]byte, error) {
	sw := bits.NewFixedSliceWriter(int(d.outInit.Size()))
	if err := d.outInit.EncodeSW(sw); err != nil {
		return nil, err
	}
	return sw.Bytes(), nil
}

// Codec returns the CMAF codec string.
func (d *HEVCData) Codec() string {
	return d.codec
}

type AACData struct {
	inInit        *mp4.InitSegment
	outInit       *mp4.InitSegment
	codec         string
	sampleRate    uint32
	channelConfig string
}

// GenCMAFInitData returns a base64 encoded CMAF initialization segment.
func (d *AACData) GenCMAFInitData() ([]byte, error) {
	sw := bits.NewFixedSliceWriter(int(d.outInit.Size()))
	err := d.outInit.EncodeSW(sw)
	if err != nil {
		return nil, err
	}
	return sw.Bytes(), nil
}

func (d *AACData) Codec() string {
	return d.codec
}

// initAACData recreates an AAC init segment from an existing init segment.
func initAACData(init *mp4.InitSegment) (*AACData, error) {
	ad := &AACData{
		inInit: init,
	}
	mp4a := init.Moov.Trak.Mdia.Minf.Stbl.Stsd.Mp4a
	esds := mp4a.Esds
	decCfg := esds.DecConfigDescriptor
	ascBytes := decCfg.DecSpecificInfo.DecConfig
	buf := bytes.NewBuffer(ascBytes)
	asc, err := aac.DecodeAudioSpecificConfig(buf)
	if err != nil {
		return nil, fmt.Errorf("could not decode audio specific config: %w", err)
	}
	objectType := asc.ObjectType
	ad.outInit = mp4.CreateEmptyInit()
	lang := init.Moov.Trak.Mdia.Mdhd.GetLanguage()
	if init.Moov.Trak.Mdia.Elng != nil {
		lang = init.Moov.Trak.Mdia.Elng.Language
	}
	timeScale := init.Moov.Trak.Mdia.Mdhd.Timescale
	ad.outInit.AddEmptyTrack(timeScale, "audio", lang)
	ad.sampleRate = uint32(mp4a.SampleRate)
	ad.channelConfig = fmt.Sprintf("%d", asc.ChannelConfiguration)
	esdsOut := mp4.CreateEsdsBox(ascBytes)
	mp4aOut := mp4.CreateAudioSampleEntryBox("mp4a",
		uint16(asc.ChannelConfiguration),
		16, uint16(ad.sampleRate), esdsOut)
	ad.outInit.Moov.Trak.Mdia.Minf.Stbl.Stsd.AddChild(mp4aOut)
	ad.codec = fmt.Sprintf("mp4a.40.%d", objectType)
	return ad, nil
}

type OpusData struct {
	inInit        *mp4.InitSegment
	outInit       *mp4.InitSegment
	codec         string
	sampleRate    uint32
	channelConfig string
}

// GenCMAFInitData returns a CMAF initialization segment for Opus.
func (d *OpusData) GenCMAFInitData() ([]byte, error) {
	sw := bits.NewFixedSliceWriter(int(d.outInit.Size()))
	err := d.outInit.EncodeSW(sw)
	if err != nil {
		return nil, err
	}
	return sw.Bytes(), nil
}

func (d *OpusData) Codec() string {
	return d.codec
}

// initOpusData recreates an Opus init segment from an existing init segment.
func initOpusData(init *mp4.InitSegment) (*OpusData, error) {
	od := &OpusData{
		inInit: init,
	}
	opus := init.Moov.Trak.Mdia.Minf.Stbl.Stsd.Opus
	if opus == nil {
		return nil, fmt.Errorf("no Opus box found in init segment")
	}
	dops := opus.Dops
	if dops == nil {
		return nil, fmt.Errorf("no dOps box found in Opus sample entry")
	}

	od.outInit = mp4.CreateEmptyInit()
	lang := init.Moov.Trak.Mdia.Mdhd.GetLanguage()
	if init.Moov.Trak.Mdia.Elng != nil {
		lang = init.Moov.Trak.Mdia.Elng.Language
	}
	timeScale := init.Moov.Trak.Mdia.Mdhd.Timescale
	od.outInit.AddEmptyTrack(timeScale, "audio", lang)
	od.sampleRate = dops.InputSampleRate
	od.channelConfig = fmt.Sprintf("%d", dops.OutputChannelCount)

	// Create new dOps box for output
	dopsOut := &mp4.DopsBox{
		Version:            dops.Version,
		OutputChannelCount: dops.OutputChannelCount,
		PreSkip:            dops.PreSkip,
		InputSampleRate:    dops.InputSampleRate,
		OutputGain:         dops.OutputGain,
		ChannelMappingFamily: dops.ChannelMappingFamily,
	}
	opusOut := mp4.CreateAudioSampleEntryBox("Opus",
		uint16(dops.OutputChannelCount),
		16, uint16(od.sampleRate), dopsOut)
	od.outInit.Moov.Trak.Mdia.Minf.Stbl.Stsd.AddChild(opusOut)
	od.codec = "Opus"
	return od, nil
}
