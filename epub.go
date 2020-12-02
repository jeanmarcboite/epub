package epub

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/rs/zerolog/log"
)

const mimetypePath = "mimetype"
const epubMimetype = "application/epub+zip"
const containerPath = "META-INF/container.xml"

var (
	ErrFileNotFound      = errors.New("epub: no '%s' found in file")
	ErrorNoISBN          = errors.New("no ISBN found in file")
	ErrorNoMimetype      = errors.New("no mimetype found in file")
	ErrorInvalidMimetype = errors.New("invalid mimetype")
	ErrorNoRootFile      = errors.New("no rootfile")

	// ErrBadRootfile occurs when container.xml references a rootfile that does
	// not exist in the zip.
	ErrorBadRootFile = errors.New("container references non-existent rootfile")
	ErrorFileMissing = errors.New("missing")
	// ErrNoItemref occurrs when a content.opf contains a spine without any
	// itemref entries.
	ErrNoItemref = errors.New("epub: no itemrefs found in spine")

	// ErrBadItemref occurs when an itemref entry in content.opf references an
	// item that does not exist in the manifest.
	ErrBadItemref = errors.New("epub: itemref references non-existent item")

	// ErrBadManifest occurs when a manifest in content.opf references an item
	// that does not exist in the zip.
	ErrBadManifest = errors.New("epub: manifest references non-existent item")
)

type EpubReader struct {
	Name  string
	Files map[string]*zip.File
	Container
}

type EpubReaderCloser struct {
	EpubReader
	file *os.File
}

// Container serves as a directory of Rootfiles.
type Container struct {
	Rootfiles []*Rootfile `xml:"rootfiles>rootfile"`
}

// Rootfile contains the location of a content.opf package file.
type Rootfile struct {
	XMLName   xml.Name `xml:"rootfile"`
	FullPath  string   `xml:"full-path,attr"`
	MediaType string   `xml:"media-type,attr"`
	Package
}

type Package struct {
	XMLName          xml.Name `xml:"package"`
	Text             string   `xml:",chardata"`
	Xmlns            string   `xml:"xmlns,attr"`
	UniqueIdentifier string   `xml:"unique-identifier,attr"`
	Version          string   `xml:"version,attr"`
	Metadata         struct {
		Text    string `xml:",chardata"`
		Dc      string `xml:"dc,attr"`
		Opf     string `xml:"opf,attr"`
		Title   string `xml:"title"`
		Creator struct {
			Text   string `xml:",chardata"`
			Role   string `xml:"role,attr"`
			FileAs string `xml:"file-as,attr"`
		} `xml:"creator"`
		Identifier []struct {
			Text   string `xml:",chardata"`
			ID     string `xml:"id,attr"`
			Scheme string `xml:"scheme,attr"`
		} `xml:"identifier"`
		Date        string `xml:"date"`
		Publisher   string `xml:"publisher"`
		Description string `xml:"description"`
		Contributor struct {
			Text string `xml:",chardata"`
			Role string `xml:"role,attr"`
		} `xml:"contributor"`
		Subject  string `xml:"subject"`
		Language string `xml:"language"`
		Meta     []struct {
			Text    string `xml:",chardata"`
			Name    string `xml:"name,attr"`
			Content string `xml:"content,attr"`
		} `xml:"meta"`
	} `xml:"metadata"`
	Manifest struct {
		Text string `xml:",chardata"`
		Item []struct {
			Text      string `xml:",chardata"`
			Href      string `xml:"href,attr"`
			ID        string `xml:"id,attr"`
			MediaType string `xml:"media-type,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		Text    string `xml:",chardata"`
		Toc     string `xml:"toc,attr"`
		Itemref []struct {
			Text  string `xml:",chardata"`
			Idref string `xml:"idref,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
	Guide struct {
		Text      string `xml:",chardata"`
		Reference []struct {
			Text  string `xml:",chardata"`
			Href  string `xml:"href,attr"`
			Title string `xml:"title,attr"`
			Type  string `xml:"type,attr"`
		} `xml:"reference"`
	} `xml:"guide"`
}

func init() {
	log.Logger = log.With().Caller().Logger()
}

func (epubReader *EpubReader) GetISBN() (string, error) {
	for _, id := range epubReader.Rootfiles[0].Metadata.Identifier {
		if id.Scheme == "ISBN" {
			if id.ID != "" {
				return id.ID, nil
			}

			return id.Text, nil
		}
	}

	return "", fmt.Errorf("epub: %s: %w", epubReader.Name, ErrorNoISBN)
}

func (epubReader *EpubReader) GetCover() (string, error) {
	return "", nil
}

func OpenReader(filename string) (*EpubReaderCloser, error) {
	zipFile, err := os.Open(filename)
	if err != nil {
		return nil, err
	}

	zipStat, err := zipFile.Stat()
	if err != nil {
		zipFile.Close()
		return nil, err
	}

	zipReader, err := zip.NewReader(zipFile, zipStat.Size())
	if err != nil {
		zipFile.Close()
		return nil, fmt.Errorf("epub: open zip %s: %w", filename, err)
	}

	reader := new(EpubReaderCloser)
	reader.Name = filename
	reader.file = zipFile

	if err = reader.init(zipReader); err != nil {
		return nil, err
	}

	return reader, nil
}

func (epubReader *EpubReader) init(zipReader *zip.Reader) error {
	epubReader.Files = make(map[string]*zip.File)
	for _, f := range zipReader.File {
		epubReader.Files[f.Name] = f
	}

	if mimetype, err := epubReader.readFile(mimetypePath); err != nil {
		log.Trace().Str("file", epubReader.Name).Msg("not an epub (no mimetype)")
		return fmt.Errorf("epub: %s: %w", epubReader.Name, ErrorNoMimetype)
	} else if mimetype.String() != epubMimetype {
		log.Trace().Str("file", epubReader.Name).Msg("not an epub (invalid mimetype)")
		return fmt.Errorf("epub: %s: %w %s", epubReader.Name, ErrorInvalidMimetype, mimetype.String())
	}

	container, err := epubReader.readFile(containerPath)
	if err != nil {
		log.Trace().Str("file", epubReader.Name).Msg("not an epub (no container)")
		return fmt.Errorf("epub: %s: %w", epubReader.Name, ErrorNoRootFile)
	}

	err = xml.Unmarshal(container.Bytes(), &epubReader.Container)
	if err != nil {
		log.Trace().Str("file", epubReader.Name).Msg(fmt.Sprintf("unmarshall container: %s", err.Error()))
		return fmt.Errorf("epub: %s: unmarshalling container: %w", epubReader.Name, err)
	}

	if len(epubReader.Container.Rootfiles) < 1 {
		return fmt.Errorf("epub: %s: %w", epubReader.Name, ErrorNoRootFile)
	}

	for _, rootFile := range epubReader.Container.Rootfiles {
		rootfile, err := epubReader.readFile(rootFile.FullPath)
		if err != nil {
			log.Trace().Str("file", epubReader.Name).Msg("not an epub (bad root file)")
			return fmt.Errorf("epub: %s: %w", epubReader.Name, ErrorBadRootFile)
		}

		err = xml.Unmarshal(rootfile.Bytes(), &rootFile.Package)
		if err != nil {
			log.Trace().Str("file", epubReader.Name).Msg("cannot parse (bad root file)")
			return fmt.Errorf("epub: cannot parse %s: %w", epubReader.Name, err)
		}
	}

	// <Rootfile full-path="OEBPS/book.opf" media-type="application/oebps-package+xml">
	//xmlm, err := xml.Marshal(epubReader.Container.Rootfiles[0])
	//fmt.Println(string(xmlm))

	log.Debug().
		Str("file", epubReader.Name).
		Str("Rootfile", epubReader.Container.Rootfiles[0].FullPath).
		Str("media-type", epubReader.Container.Rootfiles[0].MediaType).
		Msg("Epub")

	return nil
}

func (epubReader *EpubReader) readFile(name string) (*bytes.Buffer, error) {
	file, ok := epubReader.Files[name]
	if !ok {
		return nil, fmt.Errorf("epub: %s, file '%s' %w", epubReader.Name, name, ErrorFileMissing)
	}

	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	var buffer bytes.Buffer
	_, err = io.Copy(&buffer, reader)
	if err != nil {
		return nil, err
	}

	return &buffer, nil
}

func (epubReaderCloser *EpubReaderCloser) Close() {
	epubReaderCloser.file.Close()
}
