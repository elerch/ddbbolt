# DynamoDB Bolt

This project presents itself as [Amazon DynamoDB](https://aws.amazon.com/dynamodb/),
but uses [Bolt](https://github.com/boltdb/bolt) for data storage. It currently
only supports a handful of operations, and even then not with full fidelity:

* CreateTable
* BatchGetItem
* BatchWriteItem

UpdateItem, PutItem and GetItem should be trivial to implement.

It's designed for those times you want [DynamoDB Local](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.html),
but don't want a full Java VM, etc. On small data sets, this static executable
executable will use <10MB of resident memory.

# Running as Docker

Latest version can be found at [https://r.lerch.org/repo/ddbbolt/tags/](https://r.lerch.org/repo/ddbbolt/tags/).
Versions are tagged with the short hash of the git commit, and are
built as a multi-architecture image based on a scratch image.

You can run the docker image with a command like:

```sh
docker run \
  --volume=$(pwd)/ddbbolt:/data \
  -e FILE=/data/ddb.db          \
  -e PORT=8080                  \
  -p 8080:8080                  \
  -d                            \
  --name=ddbbolt                \
  --restart=unless-stopped      \
  r.lerch.org/ddbbolt:f501abe
```
