// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package project

import (
	"cmp"
	"errors"
	"net/http"
	"slices"

	issues_model "gitea.dev/models/issues"
	access_model "gitea.dev/models/perm/access"
	project_model "gitea.dev/models/project"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/json"
	"gitea.dev/modules/web"
	"gitea.dev/services/context"
	"gitea.dev/services/forms"
	project_service "gitea.dev/services/projects"
)

// findProject loads the "id" path param, scoped to whichever owner the route assigned:
// anyone else's ID reads as not found. Write permission is enforced by the route.
func findProject(ctx *context.Context) *project_model.Project {
	var project *project_model.Project
	var err error
	if ctx.Repo != nil && ctx.Repo.Repository != nil {
		project, err = project_model.GetProjectForRepoByID(ctx, ctx.Repo.Repository.ID, ctx.PathParamInt64("id"))
	} else {
		project, err = project_model.GetProjectByIDAndOwner(ctx, ctx.PathParamInt64("id"), ctx.ContextUser.ID)
	}
	if err != nil {
		ctx.NotFoundOrServerError("GetProject", project_model.IsErrProjectNotExist, err)
		return nil
	}
	return project
}

func findColumn(ctx *context.Context) (*project_model.Project, *project_model.Column) {
	project := findProject(ctx)
	if ctx.Written() {
		return nil, nil
	}
	column, err := project_model.GetColumnByIDAndProjectID(ctx, ctx.PathParamInt64("columnID"), project.ID)
	if err != nil {
		ctx.NotFoundOrServerError("GetColumnByIDAndProjectID", project_model.IsErrProjectColumnNotExist, err)
		return nil, nil
	}
	return project, column
}

func MoveColumns(ctx *context.Context) {
	project := findProject(ctx)
	if ctx.Written() {
		return
	}

	type movedColumnsForm struct {
		Columns []struct {
			ColumnID int64 `json:"columnID"`
			Sorting  int64 `json:"sorting"`
		} `json:"columns"`
	}

	form := &movedColumnsForm{}
	if err := json.NewDecoder(ctx.Req.Body).Decode(&form); err != nil {
		ctx.ServerError("DecodeMovedColumnsForm", err)
		return
	}

	sortedColumnIDs := make(map[int64]int64)
	for _, column := range form.Columns {
		sortedColumnIDs[column.Sorting] = column.ColumnID
	}

	if err := project_model.MoveColumnsOnProject(ctx, project, sortedColumnIDs); err != nil {
		ctx.ServerError("MoveColumnsOnProject", err)
		return
	}

	ctx.JSONOK()
}

func AddColumnToProjectPost(ctx *context.Context) {
	form := web.GetForm[*forms.EditProjectColumnForm](ctx)
	project := findProject(ctx)
	if ctx.Written() {
		return
	}

	if err := project_model.NewColumn(ctx, &project_model.Column{
		ProjectID: project.ID,
		Title:     form.Title,
		Color:     form.Color,
		CreatorID: ctx.Doer.ID,
	}); err != nil {
		ctx.JSONErrorAuto(err)
		return
	}

	ctx.JSONOK()
}

func EditProjectColumn(ctx *context.Context) {
	form := web.GetForm[*forms.EditProjectColumnForm](ctx)
	_, column := findColumn(ctx)
	if ctx.Written() {
		return
	}

	if form.Title != "" {
		column.Title = form.Title
	}
	column.Color = form.Color
	if form.Sorting != 0 {
		column.Sorting = form.Sorting
	}

	if err := project_model.UpdateColumn(ctx, column); err != nil {
		ctx.JSONErrorAuto(err)
		return
	}

	ctx.JSONOK()
}

func DeleteProjectColumn(ctx *context.Context) {
	_, column := findColumn(ctx)
	if ctx.Written() {
		return
	}

	if err := project_model.DeleteColumnByID(ctx, column.ID); err != nil {
		ctx.ServerError("DeleteProjectColumnByID", err)
		return
	}

	ctx.JSONOK()
}

func SetDefaultProjectColumn(ctx *context.Context) {
	project, column := findColumn(ctx)
	if ctx.Written() {
		return
	}

	if err := project_model.SetDefaultColumn(ctx, project.ID, column.ID); err != nil {
		ctx.ServerError("SetDefaultColumn", err)
		return
	}

	ctx.JSONOK()
}

func MoveIssues(ctx *context.Context) {
	project, column := findColumn(ctx)
	if ctx.Written() {
		return
	}

	type movedIssuesForm struct {
		Issues []struct {
			IssueID int64 `json:"issueID"`
			Sorting int64 `json:"sorting"`
		} `json:"issues"`
		Repos []struct {
			RepoID  int64 `json:"repoID"`
			Sorting int64 `json:"sorting"`
		} `json:"repos"`
	}

	form := &movedIssuesForm{}
	if err := json.NewDecoder(ctx.Req.Body).Decode(&form); err != nil {
		ctx.ServerError("DecodeMovedIssuesForm", err)
		return
	}

	issueIDs := make([]int64, 0, len(form.Issues))
	sortedIssueIDs := make(map[int64]int64)
	for _, issue := range form.Issues {
		issueIDs = append(issueIDs, issue.IssueID)
		sortedIssueIDs[issue.Sorting] = issue.IssueID
	}
	if len(issueIDs) > 0 {
		movedIssues, err := issues_model.GetIssuesByIDs(ctx, issueIDs)
		if err != nil {
			ctx.NotFoundOrServerError("GetIssueByID", issues_model.IsErrIssueNotExist, err)
			return
		}

		if len(movedIssues) != len(form.Issues) {
			ctx.ServerError("some issues do not exist", errors.New("some issues do not exist"))
			return
		}

		if _, err = movedIssues.LoadRepositories(ctx); err != nil {
			ctx.ServerError("LoadRepositories", err)
			return
		}

		for _, issue := range movedIssues {
			if !project.CanBeAccessedByOwnerRepo(issue.Repo.OwnerID, issue.Repo) {
				ctx.ServerError("Some issue's repoID is not equal to project's repoID", errors.New("Some issue's repoID is not equal to project's repoID"))
				return
			}
		}

		if err = project_service.MoveIssuesOnProjectColumn(ctx, ctx.Doer, column, sortedIssueIDs); err != nil {
			ctx.ServerError("MoveIssuesOnProjectColumn", err)
			return
		}
	}

	for _, repo := range form.Repos {
		if _, err := bindRepoToColumn(ctx, project, column, repo.RepoID); err != nil {
			ctx.ServerError("BindRepoToColumn", err)
			return
		}
		if err := project_model.UpdateColumnRepoSorting(ctx, column.ID, repo.RepoID, repo.Sorting); err != nil {
			ctx.ServerError("UpdateColumnRepoSorting", err)
			return
		}
	}

	ctx.JSONOK()
}

// findCardRepo returns the repository a card is added from: on a repository board the board's own
// repository, on an owner board the repository selected in the form.
func findCardRepo(ctx *context.Context, unitType unit.Type) *repo_model.Repository {
	if ctx.Repo != nil && ctx.Repo.Repository != nil {
		return ctx.Repo.Repository
	}

	repository, err := repo_model.GetRepositoryByID(ctx, ctx.FormInt64("repo"))
	if err != nil {
		ctx.NotFoundOrServerError("GetRepositoryByID", repo_model.IsErrRepoNotExist, err)
		return nil
	}

	perm, err := access_model.GetDoerRepoPermission(ctx, repository, ctx.Doer)
	if err != nil {
		ctx.ServerError("GetDoerRepoPermission", err)
		return nil
	}
	if !perm.CanRead(unitType) {
		ctx.NotFound(errors.New("no permission to read the repository"))
		return nil
	}
	return repository
}

func addCardToColumn(ctx *context.Context, isPull bool) {
	project := findProject(ctx)
	if ctx.Written() {
		return
	}

	column, err := project_model.GetColumnByIDAndProjectID(ctx, ctx.FormInt64("column_id"), project.ID)
	if err != nil {
		ctx.NotFoundOrServerError("GetColumnByIDAndProjectID", project_model.IsErrProjectColumnNotExist, err)
		return
	}

	unitType, number := unit.TypeIssues, ctx.FormInt64("issue_number")
	if isPull {
		unitType, number = unit.TypePullRequests, ctx.FormInt64("pull_number")
	}

	repository := findCardRepo(ctx, unitType)
	if ctx.Written() {
		return
	}

	issue, err := issues_model.GetIssueByIndex(ctx, repository.ID, number)
	if err != nil {
		ctx.NotFoundOrServerError("GetIssueByIndex", issues_model.IsErrIssueNotExist, err)
		return
	}
	if issue.IsPull != isPull {
		ctx.NotFound(errors.New("card type does not match the requested column"))
		return
	}

	if err := project_service.AddIssueToColumn(ctx, ctx.Doer, issue, column); err != nil {
		ctx.ServerError("AddIssueToColumn", err)
		return
	}

	if isPull {
		ctx.Flash.Success(ctx.Tr("repo.projects.column.add_pull_success", issue.Index))
	} else {
		ctx.Flash.Success(ctx.Tr("repo.projects.column.add_issue_success", issue.Index))
	}
	ctx.Redirect(project.Link(ctx))
}

func AddIssueToColumn(ctx *context.Context) {
	addCardToColumn(ctx, false)
}

func AddPullToColumn(ctx *context.Context) {
	addCardToColumn(ctx, true)
}

func UnbindIssueFromColumn(ctx *context.Context) {
	_, column := findColumn(ctx)
	if ctx.Written() {
		return
	}

	issue, err := issues_model.GetIssueByID(ctx, ctx.FormInt64("issue_id"))
	if err != nil {
		ctx.NotFoundOrServerError("GetIssueByID", issues_model.IsErrIssueNotExist, err)
		return
	}

	if err := project_service.RemoveIssueFromColumn(ctx, ctx.Doer, issue, column); err != nil {
		ctx.ServerError("RemoveIssueFromColumn", err)
		return
	}

	ctx.JSONOK()
}

// bindRepoToColumn detaches the repository from the project's other columns and binds it to the given
// column, so a repository stays in one column per project. It reports whether it already was bound there.
func bindRepoToColumn(ctx *context.Context, project *project_model.Project, column *project_model.Column, repoID int64) (bool, error) {
	columnIDs, err := project_model.GetColumnIDsByRepoID(ctx, repoID)
	if err != nil {
		return false, err
	}

	for _, columnID := range columnIDs {
		if columnID == column.ID {
			continue
		}
		otherColumn, err := project_model.GetColumn(ctx, columnID)
		if err != nil {
			if project_model.IsErrProjectColumnNotExist(err) {
				continue
			}
			return false, err
		}
		if otherColumn.ProjectID != project.ID {
			continue
		}
		if err := project_model.RemoveRepoFromColumn(ctx, columnID, repoID); err != nil {
			return false, err
		}
	}

	if slices.Contains(columnIDs, column.ID) {
		return true, nil
	}
	return false, project_model.AddRepoToColumn(ctx, column.ID, repoID)
}

func BindReposToColumn(ctx *context.Context) {
	project := findProject(ctx)
	if ctx.Written() {
		return
	}
	if project.IsRepositoryProject() {
		ctx.NotFound(errors.New("a repository project cannot bind repositories"))
		return
	}

	column, err := project_model.GetColumnByIDAndProjectID(ctx, ctx.FormInt64("column_id"), project.ID)
	if err != nil {
		ctx.NotFoundOrServerError("GetColumnByIDAndProjectID", project_model.IsErrProjectColumnNotExist, err)
		return
	}

	repoID := ctx.FormInt64("repo_ids")
	if repoID <= 0 {
		ctx.Flash.Error(ctx.Tr("repo.projects.column.choose_repos"))
		ctx.Redirect(project.Link(ctx))
		return
	}
	repository, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err != nil {
		ctx.NotFoundOrServerError("GetRepositoryByID", repo_model.IsErrRepoNotExist, err)
		return
	}
	if repository.OwnerID != project.OwnerID {
		ctx.NotFound(errors.New("repository does not belong to the project owner"))
		return
	}

	alreadyBound, err := bindRepoToColumn(ctx, project, column, repoID)
	if err != nil {
		ctx.ServerError("BindRepoToColumn", err)
		return
	}

	if alreadyBound {
		ctx.Flash.Info(ctx.Tr("repo.projects.column.repo_already_bound"))
	} else {
		ctx.Flash.Success(ctx.Tr("repo.projects.column.bind_repos_success"))
	}
	ctx.Redirect(project.Link(ctx))
}

func GetColumnRepos(ctx *context.Context) {
	_, column := findColumn(ctx)
	if ctx.Written() {
		return
	}

	repos, err := project_model.GetColumnReposByColumnID(ctx, column.ID)
	if err != nil {
		ctx.ServerError("GetColumnReposByColumnID", err)
		return
	}

	repoIDs := make([]int64, 0, len(repos))
	for _, repo := range repos {
		repoIDs = append(repoIDs, repo.ID)
	}
	ctx.JSON(http.StatusOK, repoIDs)
}

func UnbindRepoFromColumn(ctx *context.Context) {
	_, column := findColumn(ctx)
	if ctx.Written() {
		return
	}

	if err := project_model.RemoveRepoFromColumn(ctx, column.ID, ctx.FormInt64("repo_id")); err != nil {
		ctx.ServerError("RemoveRepoFromColumn", err)
		return
	}

	ctx.JSONOK()
}

type CardItem struct {
	Type    string
	Repo    *repo_model.Repository
	Issue   *issues_model.Issue
	Sorting int64
}

// BuildColumnCardsMap groups a board's cards by column: the repositories bound to an owner board plus
// the issues of the project, ordered the way the board renders them.
func BuildColumnCardsMap(ctx *context.Context, project *project_model.Project, columns []*project_model.Column, issuesMap map[int64]issues_model.IssueList) (map[int64][]*CardItem, error) {
	columnCardsMap := make(map[int64][]*CardItem, len(columns))
	for _, column := range columns {
		cards := make([]*CardItem, 0, len(issuesMap[column.ID]))
		if !project.IsRepositoryProject() {
			reposWithSorting, err := project_model.GetColumnReposWithSorting(ctx, column.ID)
			if err != nil {
				return nil, err
			}
			for _, repoWithSorting := range reposWithSorting {
				cards = append(cards, &CardItem{Type: "repo", Repo: repoWithSorting.Repo, Sorting: repoWithSorting.Sorting})
			}
		}

		projectIssues, err := column.GetIssues(ctx)
		if err != nil {
			return nil, err
		}
		for _, projectIssue := range projectIssues {
			for _, issue := range issuesMap[column.ID] {
				if issue.ID == projectIssue.IssueID {
					cards = append(cards, &CardItem{Type: "issue", Issue: issue, Sorting: projectIssue.Sorting})
					break
				}
			}
		}

		slices.SortStableFunc(cards, func(a, b *CardItem) int { return cmp.Compare(a.Sorting, b.Sorting) })
		columnCardsMap[column.ID] = cards
	}
	return columnCardsMap, nil
}
