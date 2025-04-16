# Build and cleanup management for boldbadger project

# Default recipe to show available commands
default:
    @just --list

# Build the application
build:
    go build -o boldbadger

# Clean up all generated files
clean:
    rm -f boldbadger
    rm -f image_cache.json
    rm -f imagemontage
    rm -f montage.jpg
    rm -f montage.html
    rm -f preview.html
    rm -rf images/