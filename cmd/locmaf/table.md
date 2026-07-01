# LOCMAF Overhead Experiments

Non-init overhead byte totals are measured over 10 measured group(s): 250 video samples or 460 audio samples.

Bitrate columns divide those totals by each row's measured media duration.

`locmaf_overhead_kbps` includes the LOCMAF header-id and payload-length varints.

`locmaf_overhead_kbps` uses the complete delta-compressor stream.

It includes full LOCMAF moof properties emitted immediately after resets.

`loc_header_kbps` counts MoQ subgroup object framing plus the LOC Timestamp extension header.

## Fragment Size

| #Smpl. / Chunk | #LDM / Group | CMAF OH | LOCMAF OH | CR |
|---:|---:|---:|---:|---:|
| 1 | 24 | 21.60 | 0.60 | 35.76 |
| 5 | 4 | 4.83 | 0.75 | 6.41 |
| 25 | 0 | 1.63 | 0.61 | 2.69 |

## Audio Fragment Size

| #Smpl. / Chunk | #LDM / Group | CMAF OH | LOCMAF OH | CR |
|---:|---:|---:|---:|---:|
| 1 | 45 | 40.50 | 0.91 | 44.48 |
| 2 | 22 | 20.01 | 1.66 | 12.02 |
| 46 | 0 | 2.32 | 0.92 | 2.52 |

## Init Header

| Protection | CMAF Init Bytes | LOCMAF Init Bytes | CR |
|---|---:|---:|---:|
| none | 662 | 75 | 8.83 |
| cbcs | 759 | 130 | 5.84 |
| cenc | 742 | 110 | 6.75 |

## Object Group Size

| #Obj. per Group | #LDM / Group | LOCMAF OH |
|---:|---:|---:|
| 1 | 0 | 8.22 |
| 46 | 45 | 0.91 |
| 460 | 459 | 0.76 |

## Asset Bitrate

| Asset | Mdat (kbps) | CMAF / Mdat | LOCMAF / Mdat |
|---|---:|---:|---:|
| video400avc | 373.20 | 5.79% | 0.16% |
| video600avc | 559.51 | 3.86% | 0.11% |
| video900avc | 844.50 | 2.56% | 0.07% |
| audio128aac | 128.00 | 31.64% | 0.71% |

## LOC Header

| Asset | #LOC Obj. | Mdat (kbps) | LOC OH | LOC / Mdat |
|---|---:|---:|---:|---:|
| video400avc | 250 | 373.20 | 2.61 | 0.70% |
| audio128aac | 460 | 128.00 | 4.88 | 3.81% |

## DRM

| Protection | CMAF OH | LOCMAF OH | CR |
|---|---:|---:|---:|
| none | 21.60 | 0.60 | 35.76 |
| cbcs | 34.00 | 1.50 | 22.61 |
| cenc | 37.20 | 5.56 | 6.70 |

## DRM Audio

| Protection | CMAF OH | LOCMAF OH | CR |
|---|---:|---:|---:|
| none | 40.50 | 0.91 | 44.48 |
| cbcs | 60.38 | 0.91 | 66.30 |
| cenc | 66.38 | 7.66 | 8.66 |

## Delta Header Fields - No Protection

| Asset | #LDM / Group | Field ID | Field | Count |
|---|---:|---:|---|---:|
| video400avc | 24 | 7 | sampleFlags | 10 |
| audio128aac | 45 | N/A | N/A | 0 |

## Delta Header Fields - CENC

| Asset | #LDM / Group | Field ID | Field | Count |
|---|---:|---:|---|---:|
| video400avc | 24 | 7 | sampleFlags | 10 |
| video400avc | 24 | 9 | initializationVector | 240 |
| video400avc | 24 | 13 | bytesOfClearData | 226 |
| video400avc | 24 | 15 | bytesOfProtectedData | 226 |
| audio128aac | 45 | 9 | initializationVector | 450 |

## Delta Header Fields - CBCS

| Asset | #LDM / Group | Field ID | Field | Count |
|---|---:|---:|---|---:|
| video400avc | 24 | 7 | sampleFlags | 10 |
| video400avc | 24 | 13 | bytesOfClearData | 25 |
| video400avc | 24 | 15 | bytesOfProtectedData | 239 |
| audio128aac | 45 | N/A | N/A | 0 |
