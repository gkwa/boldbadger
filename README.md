# Image Montage Creator

This Go program fetches images from a markdown file and creates a montage using ImageMagick.

## Requirements

- Go 1.18 or higher
- ImageMagick installed on your system

## How to use

1. Place your markdown file with image links in the same directory as the program, named `paste.txt`
2. Run the program:

```
go run main.go
```

3. The program will:

   - Parse the markdown file to find image URLs
   - Download all images to an `images` directory
   - Create a montage using ImageMagick
   - Generate an HTML preview file

4. Output:
   - `montage.jpg`: The combined image montage
   - `preview.html`: An HTML file to preview all images and the montage

## Notes

- The program supports both markdown image syntax and HTML img tags
- It attempts to download images concurrently for better performance
- If ImageMagick's `montage` command fails, it will try to fall back to the `convert` command
