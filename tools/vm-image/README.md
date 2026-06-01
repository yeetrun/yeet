# Yeet Ubuntu VM Image

The v0 VM payload is `vm://ubuntu/26.04`.

The current bundle version is `ubuntu-26.04-amd64-v1`. It is built from the
official Ubuntu 26.04 cloud image and boots the Ubuntu 7.0 generic kernel under
Firecracker using an initrd.

Release asset names:

- `manifest.json`
- `vmlinux`
- `initrd.img`
- `rootfs.ext4.zst`
- `firecracker`
- `checksums.txt`

The manifest URL used by catch is:

`https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-v1/manifest.json`
