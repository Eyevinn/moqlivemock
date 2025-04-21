package internal

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
)

type AVCData struct {
	inInit  *mp4.InitSegment
	outInit *mp4.InitSegment
	Spss    [][]byte
	Ppss    [][]byte
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
			case avc.NALU_IDR, avc.NALU_NON_IDR:
				binary.BigEndian.PutUint32(work, uint32(len(nalu)))
				samples[i].Data = append(samples[i].Data, work...)
				samples[i].Data = append(samples[i].Data, nalu...)
			default:
				log.Printf("dropping NALU type %s", avc.NaluType(nalu[0]))
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
	ad.outInit.Moov.Trak.SetAVCDescriptor("avc3", ad.Spss, ad.Ppss, false)
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

type AACData struct {
	inInit  *mp4.InitSegment
	outInit *mp4.InitSegment
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
	log.Printf("objectType: %d", objectType)
	ad.outInit = mp4.CreateEmptyInit()
	lang := init.Moov.Trak.Mdia.Mdhd.GetLanguage()
	if init.Moov.Trak.Mdia.Elng != nil {
		lang = init.Moov.Trak.Mdia.Elng.Language
	}
	timeScale := init.Moov.Trak.Mdia.Mdhd.Timescale
	ad.outInit.AddEmptyTrack(timeScale, "audio", lang)
	sampleRate := mp4a.SampleRate
	esdsOut := mp4.CreateEsdsBox(ascBytes)
	mp4aOut := mp4.CreateAudioSampleEntryBox("mp4a",
		uint16(asc.ChannelConfiguration),
		16, uint16(sampleRate), esdsOut)
	ad.outInit.Moov.Trak.Mdia.Minf.Stbl.Stsd.AddChild(mp4aOut)
	return ad, nil
}
