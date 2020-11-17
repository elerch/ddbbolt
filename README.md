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
