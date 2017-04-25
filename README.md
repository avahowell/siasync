# siasync

Siasync is a simple utility that syncs an on-disk folder to [Sia](https://github.com/NebulousLabs/Sia), similarly to how dropbox works. Currently this is a very simple command-line program.

## Usage

First, you must create a Sia node and form contracts with hosts. Then, simply

`siasync [path-to-folder]`

siasync will upload every file in that directory to sia and continuously sync, until stopped.


## License

The MIT License (MIT)
