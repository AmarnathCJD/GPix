package s3api

import "encoding/xml"

const s3XMLNS = "http://s3.amazonaws.com/doc/2006-03-01/"

type listAllMyBucketsResult struct {
	XMLName xml.Name      `xml:"ListAllMyBucketsResult"`
	XMLNS   string        `xml:"xmlns,attr"`
	Owner   canonicalUser `xml:"Owner"`
	Buckets bucketList    `xml:"Buckets"`
}

type bucketList struct {
	Bucket []bucketEntry `xml:"Bucket"`
}

type bucketEntry struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"`
}

type canonicalUser struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

type listObjectsV2Result struct {
	XMLName               xml.Name       `xml:"ListBucketResult"`
	XMLNS                 string         `xml:"xmlns,attr"`
	Name                  string         `xml:"Name"`
	Prefix                string         `xml:"Prefix"`
	KeyCount              int            `xml:"KeyCount"`
	MaxKeys               int            `xml:"MaxKeys"`
	Delimiter             string         `xml:"Delimiter,omitempty"`
	IsTruncated           bool           `xml:"IsTruncated"`
	ContinuationToken     string         `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string         `xml:"NextContinuationToken,omitempty"`
	Contents              []objectEntry  `xml:"Contents"`
	CommonPrefixes        []commonPrefix `xml:"CommonPrefixes,omitempty"`
}

type objectEntry struct {
	Key          string        `xml:"Key"`
	LastModified string        `xml:"LastModified"`
	ETag         string        `xml:"ETag"`
	Size         int64         `xml:"Size"`
	StorageClass string        `xml:"StorageClass"`
	Owner        canonicalUser `xml:"Owner"`
}

type commonPrefix struct {
	Prefix string `xml:"Prefix"`
}

type deleteRequest struct {
	XMLName xml.Name           `xml:"Delete"`
	Quiet   bool               `xml:"Quiet"`
	Objects []deleteObjectSpec `xml:"Object"`
}

type deleteObjectSpec struct {
	Key       string `xml:"Key"`
	VersionId string `xml:"VersionId"`
}

type deleteResult struct {
	XMLName xml.Name        `xml:"DeleteResult"`
	XMLNS   string          `xml:"xmlns,attr"`
	Deleted []deletedObject `xml:"Deleted"`
	Errors  []deleteError   `xml:"Error"`
}

type deletedObject struct {
	Key string `xml:"Key"`
}

type deleteError struct {
	Key     string `xml:"Key"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

type initiateMultipartUploadResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	XMLNS    string   `xml:"xmlns,attr"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadId string   `xml:"UploadId"`
}

type completeMultipartUpload struct {
	XMLName xml.Name             `xml:"CompleteMultipartUpload"`
	Parts   []completeUploadPart `xml:"Part"`
}

type completeUploadPart struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type completeMultipartUploadResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	XMLNS    string   `xml:"xmlns,attr"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}
