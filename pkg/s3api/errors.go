package s3api

import (
	"encoding/xml"
	"net/http"
)

type apiError struct {
	HTTPStatus int
	Code       string
	Message    string
}

func (e apiError) Error() string { return e.Code + ": " + e.Message }

type errorResponse struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource,omitempty"`
	RequestId string   `xml:"RequestId,omitempty"`
}

var (
	errAccessDenied         = apiError{http.StatusForbidden, "AccessDenied", "Access Denied"}
	errSignatureMismatch    = apiError{http.StatusForbidden, "SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided."}
	errNoSuchBucket         = apiError{http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist"}
	errNoSuchKey            = apiError{http.StatusNotFound, "NoSuchKey", "The specified key does not exist."}
	errNoSuchUpload         = apiError{http.StatusNotFound, "NoSuchUpload", "The specified multipart upload does not exist."}
	errMethodNotAllowed     = apiError{http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed against this resource."}
	errInvalidRequest       = apiError{http.StatusBadRequest, "InvalidRequest", "Invalid request."}
	errMalformedXML         = apiError{http.StatusBadRequest, "MalformedXML", "The XML you provided was not well-formed or did not validate against the published schema."}
	errEntityTooLarge       = apiError{http.StatusBadRequest, "EntityTooLarge", "Your proposed upload exceeds the maximum allowed size."}
	errInternalError        = apiError{http.StatusInternalServerError, "InternalError", "We encountered an internal error. Please try again."}
	errInvalidPart          = apiError{http.StatusBadRequest, "InvalidPart", "One or more of the specified parts could not be found."}
	errMissingContentLength = apiError{http.StatusLengthRequired, "MissingContentLength", "You must provide the Content-Length HTTP header."}
)

func writeError(w http.ResponseWriter, r *http.Request, err apiError) {
	body, mErr := xml.Marshal(errorResponse{
		Code:     err.Code,
		Message:  err.Message,
		Resource: r.URL.Path,
	})
	if mErr != nil {
		http.Error(w, err.Message, err.HTTPStatus)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(err.HTTPStatus)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(body)
}

func writeXML(w http.ResponseWriter, status int, v any) {
	body, err := xml.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(body)
}
