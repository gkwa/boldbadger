package main

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

// Logger levels
const (
	LogLevelNone = iota
	LogLevelInfo
	LogLevelDebug
)

// Logger is a custom logger with verbosity control
type Logger struct {
	level  int
	logger *log.Logger
}

// NewLogger creates a new logger with the specified level
func NewLogger(level int) *Logger {
	return &Logger{
		level:  level,
		logger: log.New(os.Stderr, "", 0),
	}
}

// Info logs a message at info level
func (l *Logger) Info(format string, v ...interface{}) {
	if l.level >= LogLevelInfo {
		l.logger.Printf("[INFO] "+format, v...)
	}
}

// Debug logs a message at debug level
func (l *Logger) Debug(format string, v ...interface{}) {
	if l.level >= LogLevelDebug {
		l.logger.Printf("[DEBUG] "+format, v...)
	}
}

// Error logs an error message
func (l *Logger) Error(format string, v ...interface{}) {
	// Errors are always shown
	l.logger.Printf("[ERROR] "+format, v...)
}

// Fatal logs a fatal error message and exits
func (l *Logger) Fatal(format string, v ...interface{}) {
	l.logger.Fatalf("[FATAL] "+format, v...)
}

// CacheEntry represents an entry in the cache
type CacheEntry struct {
	URL        string    `json:"url"`
	FilePath   string    `json:"file_path"`
	FetchedAt  time.Time `json:"fetched_at"`
	StatusCode int       `json:"status_code"`
}

// Cache manages cached image URLs
type Cache struct {
	Entries map[string]CacheEntry `json:"entries"`
	mu      sync.Mutex
	logger  *Logger
}

// NewCache creates a new cache
func NewCache(logger *Logger) *Cache {
	return &Cache{
		Entries: make(map[string]CacheEntry),
		logger:  logger,
	}
}

// Load loads the cache from a file
func (c *Cache) Load(filename string) error {
	file, err := os.Open(filename)
	if os.IsNotExist(err) {
		c.logger.Debug("Cache file does not exist yet: %s", filename)
		return nil // Cache file doesn't exist yet
	}
	if err != nil {
		return err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	return decoder.Decode(&c.Entries)
}

// Save saves the cache to a file
func (c *Cache) Save(filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(c.Entries)
}

// Get gets an entry from the cache
func (c *Cache) Get(url string) (CacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := urlToKey(url)
	entry, exists := c.Entries[key]
	return entry, exists
}

// Set sets an entry in the cache
func (c *Cache) Set(url string, entry CacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := urlToKey(url)
	c.Entries[key] = entry
}

// urlToKey converts a URL to a cache key
func urlToKey(url string) string {
	hasher := md5.New()
	hasher.Write([]byte(url))
	return hex.EncodeToString(hasher.Sum(nil))
}

// Options contains all command line options
type Options struct {
	InputFile    string
	OutputFile   string
	CacheFile    string
	NoCache      bool
	Verbosity    int
	TileGeometry string
	ImageSize    string
	Background   string
}

func main() {
	opts := &Options{}

	// Define the root command
	rootCmd := &cobra.Command{
		Use:   "montage-creator",
		Short: "Create image montages from markdown files",
		Long:  `A CLI tool to extract image URLs from markdown files, download the images, and create a montage using ImageMagick.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(opts)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Add flags
	rootCmd.Flags().StringVarP(&opts.InputFile, "input", "i", "paste.md", "Input markdown file path")
	rootCmd.Flags().StringVarP(&opts.OutputFile, "output", "o", "montage.jpg", "Output montage file path")
	rootCmd.Flags().StringVarP(&opts.CacheFile, "cache", "c", "image_cache.json", "Cache file path")
	rootCmd.Flags().BoolVar(&opts.NoCache, "no-cache", false, "Disable caching (always download fresh images)")
	rootCmd.Flags().CountVarP(&opts.Verbosity, "verbose", "v", "Enable verbose logging (use -v for info, -vv for debug)")
	rootCmd.Flags().StringVar(&opts.TileGeometry, "tile", "3x4", "Tile geometry (columns x rows)")
	rootCmd.Flags().StringVar(&opts.ImageSize, "size", "200x200", "Image size")
	rootCmd.Flags().StringVar(&opts.Background, "background", "#f5f5f5", "Background color")

	// Execute the command
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(opts *Options) error {
	// Set up logging based on verbosity
	logLevel := LogLevelNone
	if opts.Verbosity == 1 {
		logLevel = LogLevelInfo
	} else if opts.Verbosity >= 2 {
		logLevel = LogLevelDebug
	}

	logger := NewLogger(logLevel)

	logger.Debug("Starting with options: input=%s, output=%s, no-cache=%v, verbosity=%d",
		opts.InputFile, opts.OutputFile, opts.NoCache, opts.Verbosity)

	// Initialize and load the cache if not disabled
	cache := NewCache(logger)
	if !opts.NoCache {
		logger.Info("Loading cache from %s", opts.CacheFile)
		if err := cache.Load(opts.CacheFile); err != nil {
			logger.Error("Failed to load cache: %v", err)
		}
	} else {
		logger.Info("Cache disabled, all images will be freshly downloaded")
	}

	// Read the markdown content from the file
	logger.Info("Reading markdown from %s", opts.InputFile)
	content, err := os.ReadFile(opts.InputFile)
	if err != nil {
		return fmt.Errorf("error reading file: %w", err)
	}

	// Create a directory to store the downloaded images
	if err := os.MkdirAll("images", 0o755); err != nil {
		return fmt.Errorf("error creating images directory: %w", err)
	}

	// Regular expression to find image URLs in markdown
	imageRegex := regexp.MustCompile(`!\[.*?\]\((https?://[^)]+)\)`)
	matches := imageRegex.FindAllStringSubmatch(string(content), -1)

	// If no images found in the standard markdown format, try the HTML img tag format
	if len(matches) == 0 {
		// Try to find images in HTML format
		htmlImageRegex := regexp.MustCompile(`<img src="(https?://[^"]+)"`)
		matches = htmlImageRegex.FindAllStringSubmatch(string(content), -1)
	}

	if len(matches) == 0 {
		return fmt.Errorf("no images found in the markdown content")
	}

	logger.Info("Found %d image URLs in markdown", len(matches))

	// Create a wait group to synchronize goroutines
	var wg sync.WaitGroup
	// Use a mutex to protect concurrent writes to the imagePaths slice
	var mutex sync.Mutex
	var imagePaths []string

	// Download images concurrently
	for i, match := range matches {
		wg.Add(1)
		go func(i int, imageURL string) {
			defer wg.Done()

			// Clean up the URL if needed (remove query parameters)
			cleanURL := strings.Split(imageURL, "?")[0]

			// Generate a file name based on the URL hash
			hasher := md5.New()
			hasher.Write([]byte(imageURL))
			hashStr := hex.EncodeToString(hasher.Sum(nil))

			ext := filepath.Ext(cleanURL)
			if ext == "" {
				ext = ".jpg" // Default to jpg if no extension is found
			}
			fileName := fmt.Sprintf("images/%s%s", hashStr, ext)

			// Check if the image is in the cache and cache is enabled
			if !opts.NoCache {
				if entry, exists := cache.Get(imageURL); exists {
					// Check if the file exists
					if _, err := os.Stat(entry.FilePath); err == nil {
						logger.Info("[CACHE] Using cached image for %s (%s)", imageURL, entry.FilePath)
						logger.Debug("[CACHE] Cache hit details: URL=%s, Path=%s, FetchedAt=%v",
							entry.URL, entry.FilePath, entry.FetchedAt)

						mutex.Lock()
						imagePaths = append(imagePaths, entry.FilePath)
						mutex.Unlock()
						return
					} else {
						logger.Info("[CACHE] Cache entry exists but file missing for %s, will re-download", imageURL)
					}
				} else {
					logger.Debug("[CACHE] No cache entry for %s", imageURL)
				}
			}

			logger.Info("[DOWNLOAD] Fetching image %d: %s", i, imageURL)

			// Download the image
			if err := downloadImage(imageURL, fileName, logger); err != nil {
				logger.Error("[DOWNLOAD] Failed to download image %s: %v", imageURL, err)
				return
			}

			// Add to cache if caching is enabled
			if !opts.NoCache {
				entry := CacheEntry{
					URL:        imageURL,
					FilePath:   fileName,
					FetchedAt:  time.Now(),
					StatusCode: http.StatusOK,
				}
				cache.Set(imageURL, entry)
				logger.Debug("[CACHE] Added to cache: %s -> %+v", imageURL, entry)
			}

			// Add the file path to the list of images
			mutex.Lock()
			imagePaths = append(imagePaths, fileName)
			mutex.Unlock()

			logger.Info("[SUCCESS] Downloaded image %d: %s -> %s", i, imageURL, fileName)
		}(i, match[1])
	}

	// Wait for all downloads to complete
	wg.Wait()

	// Save the cache if not disabled
	if !opts.NoCache {
		logger.Info("Saving cache to %s", opts.CacheFile)
		if err := cache.Save(opts.CacheFile); err != nil {
			logger.Error("Failed to save cache: %v", err)
		}
	}

	if len(imagePaths) == 0 {
		return fmt.Errorf("no images were successfully downloaded")
	}

	logger.Info("Creating montage with %d images", len(imagePaths))

	// Prepare the command arguments - no frames, flush images
	args := append([]string{
		"-geometry", fmt.Sprintf("%s+0+0", opts.ImageSize), // Size of thumbnails with no spacing
		"-tile", opts.TileGeometry, // Tile layout (columns x rows)
		"-background", opts.Background, // Background color
		"-bordercolor", opts.Background, // Same as background to avoid visible borders
		"-mattecolor", opts.Background, // Same as background
	}, imagePaths...)
	args = append(args, opts.OutputFile) // Output file

	logger.Debug("Running ImageMagick montage command: %v", args)
	cmd := exec.Command("montage", args...)

	// Only capture output at debug level
	var cmdOutput []byte
	var cmdErr error

	if logLevel >= LogLevelDebug {
		cmdOutput, cmdErr = cmd.CombinedOutput()
	} else {
		// Discard output at lower log levels
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		cmdErr = cmd.Run()
	}

	if cmdErr != nil {
		if logLevel >= LogLevelDebug {
			logger.Error("Error creating montage: %v\n%s", cmdErr, cmdOutput)
		} else {
			logger.Error("Error creating montage: %v", cmdErr)
		}

		// Fallback to convert if montage fails
		logger.Info("Attempting to use 'convert' as a fallback...")
		convertArgs := append([]string{}, imagePaths...)
		convertArgs = append(convertArgs, "-resize", opts.ImageSize, "+append", opts.OutputFile)

		logger.Debug("Running ImageMagick convert command: %v", convertArgs)
		cmd = exec.Command("convert", convertArgs...)

		if logLevel >= LogLevelDebug {
			cmdOutput, cmdErr = cmd.CombinedOutput()
			if cmdErr != nil {
				return fmt.Errorf("error using convert fallback: %v\n%s", cmdErr, cmdOutput)
			}
		} else {
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
			if cmdErr = cmd.Run(); cmdErr != nil {
				return fmt.Errorf("error using convert fallback: %v", cmdErr)
			}
		}
	}

	logger.Info("Montage created successfully as %s", opts.OutputFile)

	// Create HTML preview file
	previewFile := strings.TrimSuffix(opts.OutputFile, filepath.Ext(opts.OutputFile)) + ".html"
	logger.Info("Generating HTML preview as %s", previewFile)
	createHTMLPreview(imagePaths, opts.OutputFile, previewFile, logger)

	return nil
}

func downloadImage(url, fileName string, logger *Logger) error {
	// Create an HTTP client with a timeout
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	logger.Debug("[DOWNLOAD] Starting download for %s -> %s", url, fileName)

	// Create the request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	// Add a user agent to avoid being blocked
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")

	logger.Debug("[DOWNLOAD] Sending HTTP request for %s", url)

	// Make the request
	startTime := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	logger.Debug("[DOWNLOAD] Received response in %v: status=%s, content-length=%s",
		time.Since(startTime), resp.Status, resp.Header.Get("Content-Length"))

	// Check if the response was successful
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	// Create the file
	file, err := os.Create(fileName)
	if err != nil {
		return err
	}
	defer file.Close()

	logger.Debug("[DOWNLOAD] Saving response body to %s", fileName)

	// Copy the body to the file
	written, err := io.Copy(file, resp.Body)
	if err != nil {
		return err
	}

	logger.Debug("[DOWNLOAD] Successfully saved %d bytes to %s", written, fileName)

	return nil
}

func createHTMLPreview(imagePaths []string, montageFile, outputFile string, logger *Logger) {
	// Create an HTML file to preview the images
	file, err := os.Create(outputFile)
	if err != nil {
		logger.Error("Error creating HTML preview: %v", err)
		return
	}
	defer file.Close()

	logger.Debug("[HTML] Creating preview with %d images and montage %s", len(imagePaths), montageFile)

	writer := bufio.NewWriter(file)
	fmt.Fprintf(writer, `<!DOCTYPE html>
<html>
<head>
    <title>Image Montage Preview</title>
    <style>
        body {
            font-family: Arial, sans-serif;
            margin: 20px;
            background-color: #f0f0f0;
        }
        h1 {
            color: #333;
        }
        .images {
            display: flex;
            flex-wrap: wrap;
            gap: 10px;
            margin-top: 20px;
        }
        .image-container {
            border: 1px solid #ddd;
            padding: 10px;
            background-color: white;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }
        img {
            max-width: 200px;
            max-height: 200px;
            display: block;
        }
        .montage {
            margin-top: 40px;
            border: 1px solid #ddd;
            padding: 10px;
            background-color: white;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
            max-width: 100%%;
        }
        .cache-info {
            font-size: 12px;
            color: #666;
            margin-top: 3px;
        }
        .cached {
            color: green;
        }
        .downloaded {
            color: blue;
        }
    </style>
</head>
<body>
    <h1>Individual Images</h1>
    <div class="images">
`)

	// Add individual images
	for i, path := range imagePaths {
		// Determine cache status from the filename
		// Since we're using hash-based filenames, we can't directly determine if an image
		// came from cache, so we'll set a default
		isCached := strings.Contains(path, "cache")
		cacheStatus := ""
		if isCached {
			cacheStatus = `<div class="cache-info cached">✓ From cache</div>`
		} else {
			cacheStatus = `<div class="cache-info downloaded">↓ Downloaded</div>`
		}

		logger.Debug("[HTML] Adding image %d: %s", i+1, path)

		fmt.Fprintf(writer, `        <div class="image-container">
            <img src="%s" alt="Image %d">
            <p>Image %d: %s</p>
            %s
        </div>
`, path, i+1, i+1, path, cacheStatus)
	}

	// Add the montage
	logger.Debug("[HTML] Adding montage: %s", montageFile)

	fmt.Fprintf(writer, `    </div>

    <h1>Montage</h1>
    <div class="montage">
        <img src="%s" alt="Montage" style="max-width: 100%%;">
    </div>
</body>
</html>
`, montageFile)

	writer.Flush()

	logger.Debug("[HTML] Preview file created successfully: %s", outputFile)
}
