// Copyright (c) 2017-2019 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package main

import (
	"html/template"
	"log"
	"regexp"

	"github.com/decred/dcrd/dcrjson"
	"github.com/decred/dcrd/rpcclient"
)

// Agenda contains all of the data representing an agenda for the html
// template programming.
type Agenda struct {
	ID              string
	Status          string
	Description     string
	Mask            uint16
	VoteVersion     uint32
	QuorumThreshold uint32
	StartHeight     int64
	EndHeight       int64
	VoteChoices     map[string]VoteChoice
	VoteCounts      map[string]uint32
}

// VoteChoice contains the details of a vote choice from an agenda,
// Each agenda will have 3 choices - yes/no/maybe
type VoteChoice struct {
	ID          string
	Description string
	Bits        uint16
}

var dcpRE = regexp.MustCompile(`(?i)DCP\-?(\d{4})`)

// Agenda status may be: started, defined, lockedin, failed, active

// IsActive indicates if the agenda is active
func (a *Agenda) IsActive() bool {
	return a.Status == "active"
}

// IsStarted indicates if the agenda is started
func (a *Agenda) IsStarted() bool {
	return a.Status == "started"
}

// IsDefined indicates if the agenda is defined
func (a *Agenda) IsDefined() bool {
	return a.Status == "defined"
}

// IsLockedIn indicates if the agenda is lockedin
func (a *Agenda) IsLockedIn() bool {
	return a.Status == "lockedin"
}

// IsFailed indicates if the agenda is failed
func (a *Agenda) IsFailed() bool {
	return a.Status == "failed"
}

// BlockLockedIn returns the height of the first block of this agenda's lock-in period. -1 if this agenda has not been locked-in.
func (a *Agenda) BlockLockedIn() int64 {
	if a.IsLockedIn() || a.IsActive() {
		return a.EndHeight + 1
	}
	return -1
}

// BlockActivated returns the height of the first block with this agenda active. -1 if this agenda has not been activated.
func (a *Agenda) BlockActivated() int64 {
	if a.IsActive() {
		return a.BlockLockedIn() + int64(activeNetParams.RuleChangeActivationInterval)
	}
	return -1
}

// TotalVotes returns the total number of No, Yes and Abstain votes cast against this agenda
func (a *Agenda) TotalVotes() uint32 {
	return a.VoteCounts["yes"] + a.VoteCounts["no"] + a.VoteCounts["abstain"]
}

// VotePercent returns the the number of yes/no/abstains votes, as a percentage of
// all votes cast against this agenda
func (a *Agenda) VotePercent(voteID string) float64 {
	percent := float64(a.VoteCounts[voteID]) / float64(a.TotalVotes())
	return toFixed(percent*100, 2)
}

// VoteCountPercentage returns the number of votes cast against this agenda as
// a percentage of the theoretical maximum number of possible votes
func (a *Agenda) VoteCountPercentage() float64 {
	maxPossibleVotes := float64(activeNetParams.RuleChangeActivationInterval) * float64(activeNetParams.TicketsPerBlock)
	voteCountPercentage := float64(a.TotalVotes()) / maxPossibleVotes
	return toFixed(voteCountPercentage*100, 1)
}

// QuorumVotePercentage returns the total number of Yes and No votes as a
// percentage of the quorum threshold.
func (a *Agenda) QuorumVotePercentage() float64 {
	quorumVotePercentage := float64(a.VoteCounts["yes"]+a.VoteCounts["no"]) / float64(a.QuorumThreshold)
	return toFixed(quorumVotePercentage*100, 1)
}

// DescriptionWithDCPURL writes a new description with an link to any DCP that
// is detected in the text.  It is written to a template.HTML type so the link
// is not escaped when the template is executed.
func (a *Agenda) DescriptionWithDCPURL() template.HTML {
	subst := `<a href="https://github.com/decred/dcps/blob/master/dcp-${1}/dcp-${1}.mediawiki" target="_blank" rel="noopener noreferrer">${0}</a>`
	return template.HTML(dcpRE.ReplaceAllString(a.Description, subst))
}

// CountVotes uses the dcrd client to find all yes/no/abstain votes
// cast against this agenda. It will count the votes and store the
// totals inside the Agenda
func (a *Agenda) countVotes(dcrdClient *rpcclient.Client, votingStartHeight int64, votingEndHeight int64) error {
	// Find the last block hash of this voting period
	// Required to call GetStakeVersions
	lastBlockHash, err := dcrdClient.GetBlockHash(votingEndHeight)
	if err != nil {
		return err
	}

	// Retrieve all votes for this voting period
	stakeVersions, err := dcrdClient.GetStakeVersions(lastBlockHash.String(), int32(votingEndHeight-votingStartHeight))
	if err != nil {
		return err
	}

	// Collect all votes of the correct version
	var votes []dcrjson.VersionBits
	for _, sVer := range stakeVersions.StakeVersions {
		for _, vote := range sVer.Votes {
			if vote.Version == a.VoteVersion {
				votes = append(votes, vote)
			}
		}
	}

	// Count the votes and store the total
	for vID := range a.VoteChoices {
		var matchingVotes uint32
		for _, vote := range votes {
			if vote.Bits&a.Mask == a.VoteChoices[vID].Bits {
				matchingVotes++
			}
		}
		a.VoteCounts[vID] = matchingVotes
		log.Printf("\t%s: %d", vID, matchingVotes)
	}

	return nil
}

// agendasFromJSON parses the response from GetVoteInfo, and
// uses the data to create a set of Agenda objects
func agendasFromJSON(getVoteInfo dcrjson.GetVoteInfoResult) []Agenda {
	var parsedAgendas []Agenda
	for _, a := range getVoteInfo.Agendas {
		voteChoices := make(map[string]VoteChoice)
		for _, choice := range a.Choices {
			vote := VoteChoice{
				ID:          choice.ID,
				Description: choice.Description,
				Bits:        choice.Bits,
			}
			voteChoices[vote.ID] = vote
		}
		parsedAgendas = append(parsedAgendas, Agenda{
			ID:              a.ID,
			Status:          a.Status,
			Description:     a.Description,
			Mask:            a.Mask,
			VoteVersion:     getVoteInfo.VoteVersion,
			QuorumThreshold: getVoteInfo.Quorum,
			VoteChoices:     voteChoices,
			VoteCounts:      make(map[string]uint32),
		})
	}
	return parsedAgendas
}

func agendasForVersions(dcrdClient *rpcclient.Client, versions []uint32, currentHeight int64, svis StakeVersionIntervals) ([]Agenda, error) {
	var allAgendas []Agenda
	for _, version := range versions {
		// Retrieve Agendas for this voting period
		getVoteInfo, err := dcrdClient.GetVoteInfo(version)
		if err != nil {
			return nil, err
		}
		agendas := agendasFromJSON(*getVoteInfo)

		// Check if upgrade to this version has occurred yet
		upgradeOccurred, upgradeHeight := svis.GetStakeVersionUpgradeHeight(version)

		if !upgradeOccurred {
			// Haven't upgraded to this stake version yet. Therefore
			// we dont know when the voting start/end heights will be.
			// Nothing more to do with these agendas
			log.Printf("Upgrade to stake version %d has not happened", version)
			allAgendas = append(allAgendas, agendas...)
			break
		}

		log.Printf("Upgrade to version %d happened at height %d", version, upgradeHeight)

		// Find the start of the next RCI after the threshold was met
		nextRCIStartHeight := activeNetParams.StakeValidationHeight
		for nextRCIStartHeight < upgradeHeight {
			nextRCIStartHeight += int64(activeNetParams.RuleChangeActivationInterval)
		}

		// Next RCI height tells us the voting start/end heights, and we can add these to the agendas
		votingStartHeight := nextRCIStartHeight
		votingEndHeight := nextRCIStartHeight + int64(activeNetParams.RuleChangeActivationInterval) - 1
		for _, agenda := range agendas {
			agenda.StartHeight = votingStartHeight
			agenda.EndHeight = votingEndHeight
			log.Printf("Voting on %s will occur between %d-%d", agenda.ID, votingStartHeight, votingEndHeight)
		}

		if votingStartHeight > currentHeight {
			// Voting hasnt started yet. So we cannot count the votes.
			// Nothing more to do with these agendas
			log.Printf("Voting is in the future so not counting votes yet")
			allAgendas = append(allAgendas, agendas...)
			break
		}

		// If agenda voting is currently in progress, only check votes up to the latest block
		if votingEndHeight > currentHeight {
			log.Printf("Voting is currently on-going")
			votingEndHeight = currentHeight
		}

		// Count votes and store totals within Agenda struct
		for _, agenda := range agendas {
			log.Printf("Counting votes for %s between blocks %d-%d",
				agenda.ID, votingStartHeight, votingEndHeight)
			err = agenda.countVotes(dcrdClient, votingStartHeight, votingEndHeight)
			if err != nil {
				return nil, err
			}
		}

		allAgendas = append(allAgendas, agendas...)
	}
	return allAgendas, nil
}