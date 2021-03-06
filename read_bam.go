package main

import (
	"io"
	"os"

	"fmt"

	"github.com/biogo/hts/bam"
	"github.com/biogo/hts/sam"
	"github.com/mingzhi/biogo/feat/gff"
)

// SamReader is an interface for sam or bam reader.
type SamReader interface {
	Header() *sam.Header
	Read() (*sam.Record, error)
}

func readSamRecords(fileName string) (headerChan chan *sam.Header, samRecChan chan *sam.Record) {
	headerChan = make(chan *sam.Header)
	samRecChan = make(chan *sam.Record)
	go func() {
		defer close(headerChan)
		defer close(samRecChan)

		// Open file stream, and close it when finished.
		f, err := os.Open(fileName)
		if err != nil {
			panic(err)
		}
		defer f.Close()

		// Decide if it is a .sam or .bam file.
		var reader SamReader
		if fileName[len(fileName)-3:] == "bam" {
			bamReader, err := bam.NewReader(f, 0)
			if err != nil {
				panic(err)
			}
			defer bamReader.Close()
			reader = bamReader
		} else {
			reader, err = sam.NewReader(f)
			if err != nil {
				panic(err)
			}
		}

		header := reader.Header()
		headerChan <- header

		// Read sam records and send them to the channel,
		// until it hit an error, which raises a panic
		// if it is not a IO EOF.
		for {
			rec, err := reader.Read()
			if err != nil {
				if err != io.EOF {
					panic(err)
				}
				break
			}
			samRecChan <- rec
		}
	}()
	return
}

// GeneSamRecords stores Sam Records.
type GeneSamRecords struct {
	ID      string
	Start   int
	End     int
	Strand  int
	Records []*sam.Record
}

// readPanGenomeBamFile reads bam file, and return the header and a channel of sam records.
func readPanGenomeBamFile(fileName string) (header *sam.Header, recordsChan chan GeneSamRecords) {
	headerChan, samRecChan := readSamRecords(fileName)
	header = <-headerChan
	recordsChan = make(chan GeneSamRecords)
	go func() {
		defer close(recordsChan)
		currentRefID := ""
		var records []*sam.Record
		for rec := range samRecChan {
			if currentRefID == "" {
				currentRefID = rec.Ref.Name()
			}
			if rec.Ref.Name() != currentRefID {
				if len(records) > 0 {
					recordsChan <- GeneSamRecords{Start: 0, Records: records, End: records[0].Ref.Len(), ID: currentRefID}
					records = []*sam.Record{}
				}
				currentRefID = rec.Ref.Name()
			}
			records = append(records, rec)
		}
		if len(records) > 0 {
			recordsChan <- GeneSamRecords{Start: 0, Records: records, End: records[0].Ref.Len()}
		}
	}()

	return
}

//readStrainBamFile read []sam.Record from a bam file of mapping reads to a strain genome file.
func readStrainBamFile(fileName string, gffMap map[string][]*gff.Record) (header *sam.Header, recordsChan chan GeneSamRecords) {
	headerChan, samRecChan := readSamRecords(fileName)
	header = <-headerChan
	recordsChan = make(chan GeneSamRecords)
	go func() {
		defer close(recordsChan)

		totalReads := 0
		filteredReads := 0
		inGeneReads := 0
		var genes []GeneSamRecords
		currentReference := ""
		for record := range samRecChan {
			totalReads++
			passed := checkReadQuality(record)
			if !passed {
				filteredReads++
				continue
			}
			if currentReference != record.Ref.Name() {
				gffRecords, found := gffMap[record.Ref.Name()]
				if !found {
					continue
				}
				currentReference = record.Ref.Name()
				genes = make([]GeneSamRecords, len(gffRecords))
				for i := range gffRecords {
					genes[i].Start = gffRecords[i].Start - 1
					genes[i].End = gffRecords[i].End
					genes[i].ID = gffRecords[i].ID()
					if gffRecords[i].Strand == gff.ReverseStrand {
						genes[i].Strand = -1
					}
				}
			}

			var maxIndex int
			for i, gene := range genes {
				if isReadInGene(record, gene) {
					inGeneReads++
					genes[i].Records = append(genes[i].Records, record)
				} else {
					if record.Pos > gene.End {
						maxIndex = i
					}

					if record.Pos+record.Len() < gene.Start {
						break
					}
				}
			}

			for i := 0; i < maxIndex; i++ {
				if len(genes[i].Records) > 0 {
					recordsChan <- genes[i]
				}
			}
			genes = genes[maxIndex:]

			if ShowProgress {
				if totalReads%10000 == 0 {
					fmt.Printf("\rProcessed %d reads.", totalReads)
				}
			}
		}

		fmt.Printf("Total reads: %d\n", totalReads)
		fmt.Printf("Filtered reads: %d\n", filteredReads)
		fmt.Printf("Reads in coding regions: %d\n", inGeneReads)

		for i := 0; i < len(genes); i++ {
			if len(genes[i].Records) > 0 {
				recordsChan <- genes[i]
			}
		}
	}()
	return
}

func isReadInGene(record *sam.Record, gffRec GeneSamRecords) bool {
	start := gffRec.Start - 1
	if record.Pos > gffRec.Start {
		start = record.Pos
	}
	end := record.Pos + record.Len()
	if record.Pos+record.Len() > gffRec.End {
		end = gffRec.End
	}

	return end > start
}

func readGffs(fileName string) map[string][]*gff.Record {
	m := make(map[string][]*gff.Record)
	f, err := os.Open(fileName)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	gffReader := gff.NewReader(f)

	records, err := gffReader.ReadAll()
	if err != nil {
		panic(err)
	}

	for _, rec := range records {
		if rec.Feature == "CDS" {
			m[rec.SeqName] = append(m[rec.SeqName], rec)
		}
	}
	return m
}

// checkReadQuality return false if the read fails quality check.
func checkReadQuality(read *sam.Record) bool {
	if int(read.MapQ) < MinMapQuality || read.Len() < MinReadLength {
		return false
	}

	// contains only match or mismatch
	for _, cigar := range read.Cigar {
		if cigar.Type() != sam.CigarMatch && cigar.Type() != sam.CigarSoftClipped {
			return false
		}
	}

	return true
}
