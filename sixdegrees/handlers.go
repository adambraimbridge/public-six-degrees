package sixdegrees

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	fthealth "github.com/Financial-Times/go-fthealth/v1_1"
	logger "github.com/Financial-Times/go-logger"
	"github.com/Financial-Times/http-handlers-go/httphandlers"
	"github.com/Financial-Times/service-status-go/gtg"
	status "github.com/Financial-Times/service-status-go/httphandlers"
	"github.com/gorilla/mux"
	metrics "github.com/rcrowley/go-metrics"
	log "github.com/sirupsen/logrus"
)

const (
	defaultConnectedPeopleResultLimit     = 10
	defaultMostMentionedPeopleResultLimit = 20
	defaultMinConnections                 = 5
	defaultContentLimit                   = 3
)

type defaultTimeGetter func() time.Time

func NewHandler(driver Driver, cacheControlHeader string) *Handler {
	return &Handler{
		driver:             driver,
		cacheControlHeader: cacheControlHeader,
	}
}

type Handler struct {
	driver             Driver
	cacheControlHeader string
}

func (hh *Handler) RegisterAdminHandlers(router *mux.Router, appSystemCode string, appName string, appDescription string, enableRequestLogging bool) http.Handler {
	timedHC := fthealth.TimedHealthCheck{
		HealthCheck: fthealth.HealthCheck{
			SystemCode:  appSystemCode,
			Name:        appName,
			Description: appDescription,
			Checks: []fthealth.Check{
				hh.HealthCheck(),
			},
		},
		Timeout: 10 * time.Second,
	}
	http.HandleFunc("/__health", fthealth.Handler(timedHC))
	http.HandleFunc(status.PingPath, status.PingHandler)
	http.HandleFunc(status.BuildInfoPath, status.BuildInfoHandler)
	http.HandleFunc("/__gtg", status.NewGoodToGoHandler(hh.GTG))

	var monitoringRouter http.Handler = router
	if enableRequestLogging {
		monitoringRouter = httphandlers.TransactionAwareRequestLoggingHandler(logger.Logger(), monitoringRouter)
	}
	monitoringRouter = httphandlers.HTTPMetricsHandler(metrics.DefaultRegistry, monitoringRouter)

	return monitoringRouter
}

func (hh *Handler) RegisterHandlers(router *mux.Router) http.Handler {
	router.HandleFunc("/sixdegrees/connectedPeople", hh.GetConnectedPeople).Methods("GET")
	router.HandleFunc("/sixdegrees/mostMentionedPeople", hh.GetMostMentionedPeople).Methods("GET")

	var monitoringRouter http.Handler = router
	monitoringRouter = httphandlers.TransactionAwareRequestLoggingHandler(logger.Logger(), monitoringRouter)
	monitoringRouter = httphandlers.HTTPMetricsHandler(metrics.DefaultRegistry, monitoringRouter)

	return monitoringRouter
}

func (hh *Handler) HealthCheck() fthealth.Check {
	return fthealth.Check{
		BusinessImpact:   "Unable to respond to Public Six Degrees",
		Name:             "Check connectivity to Neo4j - neoUrl is a parameter in hieradata for this service",
		PanicGuide:       "https://dewey.ft.com/public-six-degrees-api.html",
		Severity:         3,
		TechnicalSummary: `Cannot connect to Neo4j. If this check fails, check that Neo4j instance is up and running.`,
		Checker:          hh.Checker,
	}
}

func (hh *Handler) Checker() (string, error) {
	err := hh.driver.CheckConnectivity()
	if err == nil {
		return "Connectivity to neo4j is ok", err
	}
	return "Error connecting to neo4j", err
}

func (hh *Handler) GTG() gtg.Status {
	statusCheck := func() gtg.Status {
		return gtgCheck(hh.Checker)
	}

	return gtg.FailFastParallelCheck([]gtg.StatusChecker{statusCheck})()
}

func gtgCheck(handler func() (string, error)) gtg.Status {
	if _, err := handler(); err != nil {
		return gtg.Status{GoodToGo: false, Message: err.Error()}
	}
	return gtg.Status{GoodToGo: true}
}

func (hh *Handler) GetMostMentionedPeople(w http.ResponseWriter, r *http.Request) {
	resultLimitParam := r.URL.Query().Get("limit")
	fromDateParam := r.URL.Query().Get("fromDate")
	toDateParam := r.URL.Query().Get("toDate")

	w.Header().Set("Content-Type", "application/json; charset=UTF-8")

	limit, err := getLimit(resultLimitParam, defaultMostMentionedPeopleResultLimit)
	if err != nil {
		logger.WithError(err).Error("could not get limit")
		w.WriteHeader(http.StatusBadRequest)
		msg, _ := json.Marshal(ErrorMessage{fmt.Sprintf("Error converting limit query param, err=%v", err)})
		w.Write([]byte(msg))
		return
	}

	fromDate, toDate, err := getDateTimePeriod(fromDateParam, toDateParam)
	if err != nil {
		logger.WithError(err).Error("could not get period")
		w.WriteHeader(http.StatusBadRequest)
		msg, _ := json.Marshal(ErrorMessage{fmt.Sprintf("Error converting toDate or fromDate query params: fromDate=%s, toDate=%s", fromDateParam, toDateParam)})
		w.Write([]byte(msg))
		return
	}

	people, found, err := hh.driver.MostMentioned(fromDate.Unix(), toDate.Unix(), limit)
	if err != nil {
		logger.WithError(err).Error("could not retrieve most mentioned people")
		w.WriteHeader(http.StatusInternalServerError)
		msg, _ := json.Marshal(ErrorMessage{"Error retrieving result from DB"})
		w.Write([]byte(msg))
		return
	}
	if !found {
		w.WriteHeader(http.StatusNotFound)
		msg, _ := json.Marshal(ErrorMessage{"No result"})
		w.Write([]byte(msg))
		return
	}

	w.Header().Set("Cache-Control", hh.cacheControlHeader)
	w.WriteHeader(http.StatusOK)

	if err = json.NewEncoder(w).Encode(people); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (hh *Handler) GetConnectedPeople(w http.ResponseWriter, request *http.Request) {
	m, _ := url.ParseQuery(request.URL.RawQuery)

	minimumConnectionsParam := m.Get("minimumConnections")
	resultLimitParam := m.Get("limit")
	fromDateParam := m.Get("fromDate")
	toDateParam := m.Get("toDate")
	contentLimitParam := m.Get("contentLimit")
	uuid := m.Get("uuid")

	logger := logger.WithField("uuid", uuid)

	w.Header().Set("Content-Type", "application/json; charset=UTF-8")

	fromDate, toDate, err := getDateTimePeriod(fromDateParam, toDateParam)
	if err != nil {
		logger.WithError(err).Error("could not get period")
		w.WriteHeader(http.StatusBadRequest)
		msg, _ := json.Marshal(ErrorMessage{fmt.Sprintf("Error converting toDate or fromDate query params: fromDate=%s, toDate=%s", fromDateParam, toDateParam)})
		w.Write([]byte(msg))
		return
	}

	minimumConnections, err := getLimit(minimumConnectionsParam, defaultMinConnections)
	if err != nil {
		logger.WithError(err).Error("could not get minimum connections limit")
		w.WriteHeader(http.StatusBadRequest)
		msg, _ := json.Marshal(ErrorMessage{fmt.Sprintf("Error converting minimumConnections query param, err=%v", err)})
		w.Write([]byte(msg))
		return
	}

	resultLimit, err := getLimit(resultLimitParam, defaultConnectedPeopleResultLimit)
	if err != nil {
		logger.WithError(err).Error("could not get result limit")
		w.WriteHeader(http.StatusBadRequest)
		msg, _ := json.Marshal(ErrorMessage{fmt.Sprintf("Error converting limit query param, err=%v", err)})
		w.Write([]byte(msg))
		return
	}

	contentLimit, err := getLimit(contentLimitParam, defaultContentLimit)
	if err != nil {
		logger.WithError(err).Error("could not get content limit")
		w.WriteHeader(http.StatusBadRequest)
		msg, _ := json.Marshal(ErrorMessage{fmt.Sprintf("Error converting contentLimit query param, err=%v", err)})
		w.Write([]byte(msg))
		return
	}

	connectedPeople, found, err := hh.driver.ConnectedPeople(uuid, fromDate.Unix(), toDate.Unix(), resultLimit, minimumConnections, contentLimit)
	if err != nil {
		logger.WithError(err).Error("could not retrieve connected people")
		w.WriteHeader(http.StatusInternalServerError)
		msg, _ := json.Marshal(ErrorMessage{fmt.Sprintf("Error retrieving result for %s, err=%v", uuid, err)})
		w.Write([]byte(msg))
		return
	}

	if !found {
		w.WriteHeader(http.StatusNotFound)
		msg, _ := json.Marshal(ErrorMessage{fmt.Sprintf("No connected people found for person with uuid %s", uuid)})
		w.Write([]byte(msg))
		return
	}

	w.Header().Set("Cache-Control", hh.cacheControlHeader)
	w.WriteHeader(http.StatusOK)

	json.NewEncoder(w).Encode(connectedPeople)
}

func getDateTimePeriod(fromDateParam string, toDateParam string) (fromDate time.Time, toDate time.Time, err error) {
	fromDate, err = getDate(fromDateParam, getDefaultFromDate)
	if err != nil {
		return
	}

	toDate, err = getDate(toDateParam, getDefaultToDate)
	if err != nil {
		return
	}

	//toDate cannot be earlier than fromDate, defaulting fromDate to a week from toDate
	if toDate.Before(fromDate) {
		fromDate = toDate.AddDate(0, 0, -7)
	}

	// Restrict query for 1 year period based on fromDate value
	fromDatePlusAYear := fromDate.AddDate(1, 0, 0)
	if fromDatePlusAYear.Before(toDate) {
		toDate = fromDatePlusAYear
	}

	log.Debugf("The given period is from %v to %v\n", fromDate.String(), toDate.String())
	return
}

func getDate(dateParam string, getDefaultTime defaultTimeGetter) (time.Time, error) {
	if dateParam == "" {
		return getDefaultTime(), nil
	}
	return convertDateStringToDateTime(dateParam)
}

func getLimit(limitParam string, defaultLimit int) (int, error) {
	if limitParam == "" {
		return defaultLimit, nil
	}
	return strconv.Atoi(limitParam)
}

func convertDateStringToDateTime(dateString string) (time.Time, error) {
	datetime, err := time.Parse("2006-01-02", dateString)

	if err != nil {
		return time.Time{}, err
	}

	return datetime, nil
}

func getDefaultFromDate() time.Time {
	return time.Now().AddDate(0, 0, -7)
}

func getDefaultToDate() time.Time {
	return time.Now()
}
