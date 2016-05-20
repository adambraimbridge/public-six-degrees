package main

import (
	"fmt"

	"github.com/Financial-Times/neo-model-utils-go/mapper"
	log "github.com/Sirupsen/logrus"
	"github.com/jmcvetta/neoism"
)
func (pcw CypherDriver) MostMentioned(fromDateEpoch int64, toDateEpoch int64, limit int) (thingList []Thing, found bool, err error) {
	log.Infof("logging fromDate:%v toDate:%v  limit:%v",fromDateEpoch, toDateEpoch, limit)
	results := []neoMentionsReadStruct{}
	query := &neoism.CypherQuery{
		Statement: `MATCH (c:Content)-[a:MENTIONS]->(p:Person)
					WHERE c.publishedDateEpoch > {fromDateEpoch} AND c.publishedDateEpoch < {toDateEpoch}
					WITH p.prefLabel as prefLabel, p.uuid as uuid,
					COUNT(a) as mentions
					RETURN uuid, prefLabel, mentions
					ORDER BY mentions
					DESC LIMIT {mentionsLimit}`,
		Parameters: neoism.Props{"fromDateEpoch": fromDateEpoch, "toDateEpoch": toDateEpoch, "mentionsLimit": limit},
		Result:     &results,
	}
	log.Infof("Query %v", query)

	err = pcw.db.Cypher(query)
	log.Infof("Results%v", &results)

	if err != nil {
		log.Errorf("Error finding %v most mentioned people between %v and %v with the following statement: %v  Error: %v", limit, fromDateEpoch, toDateEpoch, query.Statement, err)
		return []Thing{}, false, fmt.Errorf("Error finding %v most mentioned people between %v and %v", limit, fromDateEpoch, toDateEpoch)
	}
	log.Debugf("CypherResult MostMentioned was (fromDate=%v, toDate=%v): %+v", limit, fromDateEpoch, toDateEpoch, results)

	thingList, _ = neoReadStructToMentionPeople(&results, limit, pcw.env)
	log.Infof("Returning %v", thingList)
	return thingList, true, nil
}

func neoReadStructToMentionPeople(neo *[]neoMentionsReadStruct, limit int, env string) (peopleList []Thing, err error) {
	peopleList = []Thing{}
	for _, neoCon := range *neo {
		log.Infof("neoCon result: %v", neoCon)
		var thing = Thing{}
		thing.ID = mapper.IDURL(neoCon.UUID)
		thing.PrefLabel = neoCon.PrefLabel
		peopleList = append(peopleList, thing)
	}
	return peopleList, nil
}