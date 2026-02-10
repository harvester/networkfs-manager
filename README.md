networkfs-manager
========

Network FS(Filesystem) Manager helps manage the network filesystem. Now, it only supports Longhorn RWX volume (NFS).

## Building

`make`

This will build both amd64 and arm64 binaries, plus a container image
which will be named something like `harvester/networkfs-manager:dev`.

To build a container image and push it to your own repo on dockerhub, do this:

```sh
export REPO="your dockerhub username"
make
docker push $REPO/networkfs-manager:dev
```

## Features
- [x] Support Longhorn RWX Volume (NFS)
- [ ] Support General NFS Filesystem
- [ ] Support CIFS/SMB Filesystem
- [ ] Support CephFS

## Overview

Network FS Manager could generate the network filesystem information like endpoint, path, and recommended mount options for the user to mount the network filesystem. The information was collected from the endpoint or specific CR from the storage provider (e.g., ShareManager CR from Longhorn).

## Architecture

The **Network FS Manager** is a Kubernetes controller developed by Go and leverages Rancher's [wrangler](https://github.com/rancher/wrangler/) framework.

**networkfs-manager** is a single binary built by Go and could be deployed as a Kubernetes DaemonSet. You can also install it as a Helm chart.

The **Network FS Manager** mainly focuses on the CR `Endpoint` and `ShareManager` from Longhorn. And provides its own CR `Networkfilesystem` to store the network filesystem information.

### `Networkfilesystem` Custom Resource(CR)

The `Networkfilesystem` CR is used to store the corresponding network filesystem information. The CR structure is as follows:

    apiVersion: harvesterhci.io/v1beta1
    kind: NetworkFilesystem
    metadata: 
        name: pvc-76d28565-fa65-4b37-962b-62da3ee668f8
        namespace: harvester-system
    spec:
        desiredState: Disabled
        networkFSName: pvc-76d28565-fa65-4b37-962b-62da3ee668f8
    status:
        conditions:
        endpoint: ""
        state: Disabled
        status: NotReady
        mountOptions: ""    
        type: NFS


The `desiredState` is used to control the state of the network filesystem. Usually `Enabled` or `Disabled`. The `networkFSName` is the name (usually path) of the network filesystem and is used for the mount path, like the entry point. The `state` is the state that the current state compares with the `desiredState`. The `status` is the status of the network filesystem. The `mountOptions` are the recommended mount options to hint the user to mount the network filesystem. The `type` is the type of the network filesystem. Now, only support `NFS`.

## License
Copyright 2024-2026 [SUSE, LLC.](https://www.suse.com/)

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

[http://www.apache.org/licenses/LICENSE-2.0](http://www.apache.org/licenses/LICENSE-2.0)

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.